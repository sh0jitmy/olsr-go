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

package netlink

import (
	"context"
	"net"
	"sync"

	"github.com/sh0jitmy/olsr-go/internal/eventbus"
	vnetlink "github.com/vishvananda/netlink"
)

type LinuxMonitor struct {
	mu       sync.RWMutex
	eventBus *eventbus.EventBus
	ifaces   map[string]*InterfaceInfo
	ctx      context.Context
	cancel   context.CancelFunc
}

func NewMonitor(bus *eventbus.EventBus) Monitor {
	return &LinuxMonitor{
		eventBus: bus,
		ifaces:   make(map[string]*InterfaceInfo),
	}
}

func (m *LinuxMonitor) Start(ctx context.Context) {
	m.ctx, m.cancel = context.WithCancel(ctx)
	m.scanInterfaces()

	// Subscribe to link and address changes
	linkChan := make(chan vnetlink.LinkUpdate)
	addrChan := make(chan vnetlink.AddrUpdate)

	if err := vnetlink.LinkSubscribe(linkChan, m.ctx.Done()); err != nil {
		// Log or fallback
	}
	if err := vnetlink.AddrSubscribe(addrChan, m.ctx.Done()); err != nil {
		// Log or fallback
	}

	go func() {
		for {
			select {
			case <-m.ctx.Done():
				return
			case <-linkChan:
				m.scanInterfaces()
				m.eventBus.Publish(m.ctx, eventbus.Event{
					Type: eventbus.EventInterfaceUpdate,
					Data: "link_changed",
				})
			case <-addrChan:
				m.scanInterfaces()
				m.eventBus.Publish(m.ctx, eventbus.Event{
					Type: eventbus.EventInterfaceUpdate,
					Data: "address_changed",
				})
			}
		}
	}()
}

func (m *LinuxMonitor) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
}

func (m *LinuxMonitor) scanInterfaces() {
	m.mu.Lock()
	defer m.mu.Unlock()

	links, err := vnetlink.LinkList()
	if err != nil {
		return
	}

	newIfaces := make(map[string]*InterfaceInfo)

	for _, link := range links {
		attrs := link.Attrs()
		isUp := (attrs.Flags & net.FlagUp) != 0

		addrs, err := vnetlink.AddrList(link, vnetlink.FAMILY_V4)
		ips := make([]string, 0)
		if err == nil {
			for _, addr := range addrs {
				ips = append(ips, addr.IP.String())
			}
		}

		newIfaces[attrs.Name] = &InterfaceInfo{
			Name:  attrs.Name,
			Index: attrs.Index,
			IPs:   ips,
			MTU:   attrs.MTU,
			IsUp:  isUp,
		}
	}

	m.ifaces = newIfaces
}

func (m *LinuxMonitor) GetInterfaces() []InterfaceInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	list := make([]InterfaceInfo, 0, len(m.ifaces))
	for _, info := range m.ifaces {
		list = append(list, *info)
	}
	return list
}

func (m *LinuxMonitor) GetIfaceIndex(name string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if info, ok := m.ifaces[name]; ok {
		return info.Index
	}
	return 0
}

func (m *LinuxMonitor) GetIfaceName(index int) string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for name, info := range m.ifaces {
		if info.Index == index {
			return name
		}
	}
	return ""
}
