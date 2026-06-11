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
	"context"
	"fmt"
	"log/slog"
	"net"
	"os/exec"
	"strings"
	"sync"

	"github.com/florianl/go-nfqueue"
)

type NFTablesManager struct {
	mu         sync.Mutex
	interfaces []string
	routes     map[string]string // Key: "srcIP:grpIP" -> value: "mac"
	execCmd    func(name string, arg ...string) *exec.Cmd
}

func NewNFTablesManager() LoopPreventionManager {
	return &NFTablesManager{
		routes:  make(map[string]string),
		execCmd: exec.Command,
	}
}

func (m *NFTablesManager) Start(ctx context.Context, interfaces []string) error {
	m.mu.Lock()
	m.interfaces = interfaces
	m.mu.Unlock()

	slog.Info("Starting nftables loop prevention manager")
	cleanupCmd := m.execCmd("nft", "delete", "table", "inet", "molsr_filter")
	_ = cleanupCmd.Run() // Ignore error

	createTableCmd := m.execCmd("nft", "add", "table", "inet", "molsr_filter")
	if err := createTableCmd.Run(); err != nil {
		return fmt.Errorf("failed to create nftables table: %w", err)
	}

	createChainCmd := m.execCmd("nft", "add", "chain", "inet", "molsr_filter", "prerouting", "{ type filter hook prerouting priority -150 ; }")
	if err := createChainCmd.Run(); err != nil {
		return fmt.Errorf("failed to create nftables prerouting chain: %w", err)
	}

	return nil
}

func (m *NFTablesManager) Stop() error {
	slog.Info("Stopping nftables loop prevention manager")
	cleanupCmd := m.execCmd("nft", "delete", "table", "inet", "molsr_filter")
	_ = cleanupCmd.Run() // Ignore error
	return nil
}

func (m *NFTablesManager) OnMulticastRouteAdd(srcIP, grpIP string, nextHopIP string) error {
	mac, err := ResolveMACFromARP(nextHopIP)
	if err != nil {
		return fmt.Errorf("failed to resolve next hop MAC for IP %s: %w", nextHopIP, err)
	}

	m.mu.Lock()
	m.routes[srcIP+":"+grpIP] = strings.ToLower(mac)
	m.mu.Unlock()

	return m.applyRules()
}

func (m *NFTablesManager) OnMulticastRouteDelete(srcIP, grpIP string) error {
	m.mu.Lock()
	delete(m.routes, srcIP+":"+grpIP)
	m.mu.Unlock()

	return m.applyRules()
}

func (m *NFTablesManager) applyRules() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	flushCmd := m.execCmd("nft", "flush", "chain", "inet", "molsr_filter", "prerouting")
	if err := flushCmd.Run(); err != nil {
		slog.Error("Failed to flush nftables prerouting chain", "error", err)
	}

	for routeKey, mac := range m.routes {
		parts := strings.Split(routeKey, ":")
		if len(parts) != 2 {
			continue
		}
		srcIP := parts[0]
		grpIP := parts[1]

		for _, iface := range m.interfaces {
			cmdAccept := m.execCmd("nft", "add", "rule", "inet", "molsr_filter", "prerouting",
				"iifname", iface, "ip", "saddr", srcIP, "ip", "daddr", grpIP, "ether", "saddr", mac, "accept")
			if err := cmdAccept.Run(); err != nil {
				slog.Error("Failed to add nftables accept rule", "iface", iface, "src", srcIP, "grp", grpIP, "mac", mac, "error", err)
			}

			cmdDrop := m.execCmd("nft", "add", "rule", "inet", "molsr_filter", "prerouting",
				"iifname", iface, "ip", "saddr", srcIP, "ip", "daddr", grpIP, "drop")
			if err := cmdDrop.Run(); err != nil {
				slog.Error("Failed to add nftables drop rule", "iface", iface, "src", srcIP, "grp", grpIP, "error", err)
			}
		}
	}

	return nil
}

type NFQueueManager struct {
	mu         sync.RWMutex
	interfaces []string
	routes     map[string]string // Key: "srcIP:grpIP" -> value: "mac"
	execCmd    func(name string, arg ...string) *exec.Cmd
	nfq        *nfqueue.Nfqueue
	cancel     context.CancelFunc
}

func NewNFQueueManager() LoopPreventionManager {
	return &NFQueueManager{
		routes:  make(map[string]string),
		execCmd: exec.Command,
	}
}

func (m *NFQueueManager) Start(ctx context.Context, interfaces []string) error {
	m.mu.Lock()
	m.interfaces = interfaces
	m.mu.Unlock()

	slog.Info("Starting NFQUEUE loop prevention manager")

	cleanupCmd := m.execCmd("nft", "delete", "table", "inet", "molsr_filter")
	_ = cleanupCmd.Run() // Ignore error

	createTableCmd := m.execCmd("nft", "add", "table", "inet", "molsr_filter")
	if err := createTableCmd.Run(); err != nil {
		return fmt.Errorf("failed to create nftables table: %w", err)
	}

	createChainCmd := m.execCmd("nft", "add", "chain", "inet", "molsr_filter", "prerouting", "{ type filter hook prerouting priority -150 ; }")
	if err := createChainCmd.Run(); err != nil {
		return fmt.Errorf("failed to create nftables prerouting chain: %w", err)
	}

	for _, iface := range interfaces {
		queueCmd := m.execCmd("nft", "add", "rule", "inet", "molsr_filter", "prerouting", "iifname", iface, "ip", "daddr", "224.0.0.0/4", "queue", "num", "1")
		if err := queueCmd.Run(); err != nil {
			return fmt.Errorf("failed to add nftables queue rule for %s: %w", iface, err)
		}
	}

	config := nfqueue.Config{
		NfQueue:      1,
		Copymode:     nfqueue.NfQnlCopyPacket,
		MaxPacketLen: 0xFFFF,
		MaxQueueLen:  0xFF,
	}

	nf, err := nfqueue.Open(&config)
	if err != nil {
		return fmt.Errorf("failed to open NFQUEUE 1: %w (make sure you have CAP_NET_ADMIN)", err)
	}
	m.nfq = nf

	var runCtx context.Context
	runCtx, m.cancel = context.WithCancel(ctx)

	go func() {
		err = m.nfq.RegisterWithErrorFunc(runCtx, m.hookFunc, func(err error) int {
			slog.Error("NFQUEUE netlink error", "error", err)
			return 0
		})
		if err != nil {
			slog.Error("Failed to register NFQUEUE hook", "error", err)
		}
	}()

	return nil
}

func (m *NFQueueManager) Stop() error {
	slog.Info("Stopping NFQUEUE loop prevention manager")
	if m.cancel != nil {
		m.cancel()
	}
	if m.nfq != nil {
		_ = m.nfq.Close()
	}

	cleanupCmd := m.execCmd("nft", "delete", "table", "inet", "molsr_filter")
	_ = cleanupCmd.Run() // Ignore error

	return nil
}

func (m *NFQueueManager) OnMulticastRouteAdd(srcIP, grpIP string, nextHopIP string) error {
	mac, err := ResolveMACFromARP(nextHopIP)
	if err != nil {
		return fmt.Errorf("failed to resolve next hop MAC for IP %s: %w", nextHopIP, err)
	}

	m.mu.Lock()
	m.routes[srcIP+":"+grpIP] = strings.ToLower(mac)
	m.mu.Unlock()

	slog.Info("NFQUEUE registered multicast route", "src", srcIP, "grp", grpIP, "mac", mac)
	return nil
}

func (m *NFQueueManager) OnMulticastRouteDelete(srcIP, grpIP string) error {
	m.mu.Lock()
	delete(m.routes, srcIP+":"+grpIP)
	m.mu.Unlock()

	slog.Info("NFQUEUE unregistered multicast route", "src", srcIP, "grp", grpIP)
	return nil
}

func (m *NFQueueManager) hookFunc(attr nfqueue.Attribute) int {
	if attr.PacketID == nil || attr.Payload == nil {
		return 0
	}

	packetID := *attr.PacketID
	payload := *attr.Payload

	if len(payload) < 20 {
		_ = m.nfq.SetVerdict(packetID, nfqueue.NfAccept)
		return 0
	}

	version := payload[0] >> 4
	if version != 4 {
		_ = m.nfq.SetVerdict(packetID, nfqueue.NfAccept)
		return 0
	}

	srcIP := net.IPv4(payload[12], payload[13], payload[14], payload[15]).String()
	dstIP := net.IPv4(payload[16], payload[17], payload[18], payload[19]).String()

	m.mu.RLock()
	expectedMac, exists := m.routes[srcIP+":"+dstIP]
	m.mu.RUnlock()

	if !exists {
		_ = m.nfq.SetVerdict(packetID, nfqueue.NfAccept)
		return 0
	}

	if attr.HwAddr == nil {
		_ = m.nfq.SetVerdict(packetID, nfqueue.NfDrop)
		return 0
	}

	actualMac := formatMAC(*attr.HwAddr)
	if actualMac == expectedMac {
		_ = m.nfq.SetVerdict(packetID, nfqueue.NfAccept)
	} else {
		slog.Debug("NFQUEUE loop prevention: dropping duplicate packet", "src", srcIP, "grp", dstIP, "expected", expectedMac, "actual", actualMac)
		_ = m.nfq.SetVerdict(packetID, nfqueue.NfDrop)
	}

	return 0
}

func formatMAC(mac []byte) string {
	if len(mac) == 0 {
		return ""
	}
	parts := make([]string, len(mac))
	for i, b := range mac {
		parts[i] = fmt.Sprintf("%02x", b)
	}
	return strings.Join(parts, ":")
}
