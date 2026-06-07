// Copyright 2026 The olsrd-go Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build linux

package mroute

import (
	"fmt"
	"log/slog"
	"net"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Multicast routing socket options (from <linux/mroute.h>)
const (
	MRT_INIT    = 200
	MRT_DONE    = 201
	MRT_ADD_VIF = 202
	MRT_DEL_VIF = 203
	MRT_ADD_MFC = 204
	MRT_DEL_MFC = 205
)

// VIFF_USE_IFINDEX flag
const (
	VIFF_USE_IFINDEX = 0x8
)

type vifctl struct {
	vifc_vifi        uint16
	vifc_flags       uint8
	vifc_threshold   uint8
	vifc_rate_limit  uint32
	vifc_lcl_ifindex int32
	vifc_rmt_addr    [4]byte
}

type mfcctl struct {
	mfcc_origin   [4]byte
	mfcc_mcastgrp [4]byte
	mfcc_parent   uint16
	mfcc_ttls     [32]uint8
	_             [2]byte // 2 bytes padding
	mfcc_pkt_cnt  uint32
	mfcc_byte_cnt uint32
	mfcc_wrong_if uint32
	mfcc_expire   int32
}

type LinuxMulticastRouter struct {
	mu      sync.Mutex
	fd      int
	vifMap  map[int]uint16 // maps Linux ifindex -> VIF index
	nextVif uint16
}

func NewMulticastRouter() MulticastRouter {
	return &LinuxMulticastRouter{
		fd:     -1,
		vifMap: make(map[int]uint16),
	}
}

func (r *LinuxMulticastRouter) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.fd != -1 {
		return nil
	}

	// Open raw IGMP socket
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_RAW, unix.IPPROTO_IGMP)
	if err != nil {
		return fmt.Errorf("failed to open raw IGMP socket: %w", err)
	}

	// Initialize multicast routing
	err = unix.SetsockoptInt(fd, unix.IPPROTO_IP, MRT_INIT, 1)
	if err != nil {
		unix.Close(fd)
		return fmt.Errorf("failed to initialize multicast routing (MRT_INIT): %w (make sure no other multicast daemon like pimd is running and process has CAP_NET_ADMIN)", err)
	}

	r.fd = fd
	slog.Info("Kernel multicast routing initialized successfully")
	return nil
}

func (r *LinuxMulticastRouter) Stop() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.fd == -1 {
		return nil
	}

	// Closing the socket automatically cleans up all kernel VIFs and MFCs
	unix.Close(r.fd)
	r.fd = -1
	r.vifMap = make(map[int]uint16)
	r.nextVif = 0
	slog.Info("Kernel multicast routing stopped and cleaned up")
	return nil
}

func (r *LinuxMulticastRouter) registerVif(ifindex int) (uint16, error) {
	if vif, ok := r.vifMap[ifindex]; ok {
		return vif, nil
	}

	if r.nextVif >= 32 {
		return 0, fmt.Errorf("maximum number of virtual interfaces (32) reached")
	}

	vif := r.nextVif
	r.nextVif++

	vc := vifctl{
		vifc_vifi:        vif,
		vifc_flags:       VIFF_USE_IFINDEX,
		vifc_threshold:   1,
		vifc_lcl_ifindex: int32(ifindex),
	}

	err := setsockoptStruct(r.fd, unix.IPPROTO_IP, MRT_ADD_VIF, unsafe.Pointer(&vc), unsafe.Sizeof(vc))
	if err != nil {
		return 0, fmt.Errorf("failed to add VIF %d for interface index %d: %w", vif, ifindex, err)
	}

	r.vifMap[ifindex] = vif
	slog.Info("Registered virtual multicast interface", "vif", vif, "ifindex", ifindex)
	return vif, nil
}

func (r *LinuxMulticastRouter) AddMulticastRoute(srcIP, grpIP string, iif int, oifs []int) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.fd == -1 {
		return fmt.Errorf("multicast router not started")
	}

	src := net.ParseIP(srcIP).To4()
	grp := net.ParseIP(grpIP).To4()
	if src == nil || grp == nil {
		return fmt.Errorf("invalid IPv4 addresses: src=%s grp=%s", srcIP, grpIP)
	}

	// Register incoming interface (IIF) as VIF
	parentVif, err := r.registerVif(iif)
	if err != nil {
		return fmt.Errorf("failed to register IIF: %w", err)
	}

	// Register outgoing interfaces (OIFs) as VIFs, and build TTL array
	var ttls [32]uint8
	for _, oif := range oifs {
		oifVif, err := r.registerVif(oif)
		if err != nil {
			return fmt.Errorf("failed to register OIF: %w", err)
		}
		ttls[oifVif] = 1
	}

	mc := mfcctl{
		mfcc_parent: parentVif,
		mfcc_ttls:   ttls,
	}
	copy(mc.mfcc_origin[:], src)
	copy(mc.mfcc_mcastgrp[:], grp)

	err = setsockoptStruct(r.fd, unix.IPPROTO_IP, MRT_ADD_MFC, unsafe.Pointer(&mc), unsafe.Sizeof(mc))
	if err != nil {
		return fmt.Errorf("failed to add multicast forwarding cache entry: %w", err)
	}

	slog.Info("Added kernel multicast route", "src", srcIP, "grp", grpIP, "iif", iif, "oifs", oifs)
	return nil
}

func (r *LinuxMulticastRouter) DeleteMulticastRoute(srcIP, grpIP string, iif int, oifs []int) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.fd == -1 {
		return fmt.Errorf("multicast router not started")
	}

	src := net.ParseIP(srcIP).To4()
	grp := net.ParseIP(grpIP).To4()
	if src == nil || grp == nil {
		return fmt.Errorf("invalid IPv4 addresses: src=%s grp=%s", srcIP, grpIP)
	}

	parentVif, ok := r.vifMap[iif]
	if !ok {
		// If IIF wasn't registered as a VIF, it means no route was successfully added
		return nil
	}

	mc := mfcctl{
		mfcc_parent: parentVif,
	}
	copy(mc.mfcc_origin[:], src)
	copy(mc.mfcc_mcastgrp[:], grp)

	err := setsockoptStruct(r.fd, unix.IPPROTO_IP, MRT_DEL_MFC, unsafe.Pointer(&mc), unsafe.Sizeof(mc))
	if err != nil {
		return fmt.Errorf("failed to delete multicast forwarding cache entry: %w", err)
	}

	slog.Info("Deleted kernel multicast route", "src", srcIP, "grp", grpIP, "iif", iif)
	return nil
}

func setsockoptStruct(fd int, level int, optname int, ptr unsafe.Pointer, size uintptr) error {
	_, _, errno := unix.Syscall6(
		unix.SYS_SETSOCKOPT,
		uintptr(fd),
		uintptr(level),
		uintptr(optname),
		uintptr(ptr),
		size,
		0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}
