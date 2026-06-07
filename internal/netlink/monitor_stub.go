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

//go:build !linux

package netlink

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/sh0jitmy/olsr-go/internal/eventbus"
)

type StubMonitor struct {
	mu       sync.RWMutex
	eventBus *eventbus.EventBus
	ifaces   map[string]*InterfaceInfo
	ctx      context.Context
	cancel   context.CancelFunc
}

func NewMonitor(bus *eventbus.EventBus) Monitor {
	return &StubMonitor{
		eventBus: bus,
		ifaces:   make(map[string]*InterfaceInfo),
	}
}

func (m *StubMonitor) Start(ctx context.Context) {
	m.ctx, m.cancel = context.WithCancel(ctx)
	m.scanInterfaces()

	// Since we don't have netlink on non-Linux, we poll interfaces periodically
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-m.ctx.Done():
				return
			case <-ticker.C:
				changed := m.scanInterfaces()
				if changed {
					m.eventBus.Publish(m.ctx, eventbus.Event{
						Type: eventbus.EventInterfaceUpdate,
						Data: "stub_polling_changed",
					})
				}
			}
		}
	}()
}

func (m *StubMonitor) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
}

func (m *StubMonitor) scanInterfaces() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	netIfaces, err := net.Interfaces()
	if err != nil {
		return false
	}

	newIfaces := make(map[string]*InterfaceInfo)
	changed := false

	for _, iface := range netIfaces {
		isUp := (iface.Flags & net.FlagUp) != 0
		addrs, err := iface.Addrs()
		ips := make([]string, 0)
		if err == nil {
			for _, addr := range addrs {
				if ipNet, ok := addr.(*net.IPNet); ok {
					if ipNet.IP.To4() != nil {
						ips = append(ips, ipNet.IP.String())
					}
				}
			}
		}

		info := &InterfaceInfo{
			Name:  iface.Name,
			Index: iface.Index,
			IPs:   ips,
			MTU:   iface.MTU,
			IsUp:  isUp,
		}

		newIfaces[iface.Name] = info

		// Simple change detection
		old, exists := m.ifaces[iface.Name]
		if !exists || old.IsUp != info.IsUp || len(old.IPs) != len(info.IPs) || old.MTU != info.MTU {
			changed = true
		}
	}

	if len(m.ifaces) != len(newIfaces) {
		changed = true
	}

	m.ifaces = newIfaces
	return changed
}

func (m *StubMonitor) GetInterfaces() []InterfaceInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	list := make([]InterfaceInfo, 0, len(m.ifaces))
	for _, info := range m.ifaces {
		list = append(list, *info)
	}
	return list
}

func (m *StubMonitor) GetIfaceIndex(name string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if info, ok := m.ifaces[name]; ok {
		return info.Index
	}
	return 0
}

func (m *StubMonitor) GetIfaceName(index int) string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for name, info := range m.ifaces {
		if info.Index == index {
			return name
		}
	}
	return ""
}
