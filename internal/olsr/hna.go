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

package olsr

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/shjtmy/olsr-go/internal/eventbus"
)

type HNAEntry struct {
	GatewayIP      string
	Network        net.IPNet
	ExpirationTime time.Time
	IsLocal        bool
}

type HNAManager struct {
	mu        sync.RWMutex
	localHNAs map[string]net.IPNet      // Key: network CIDR string
	hnaSet    map[string]map[string]*HNAEntry // Key: network CIDR string -> Gateway IP -> Entry
	eventBus  *eventbus.EventBus
}

func NewHNAManager(bus *eventbus.EventBus) *HNAManager {
	return &HNAManager{
		localHNAs: make(map[string]net.IPNet),
		hnaSet:    make(map[string]map[string]*HNAEntry),
		eventBus:  bus,
	}
}

// AddLocalHNA registers a network prefix to be advertised by this node.
func (h *HNAManager) AddLocalHNA(ctx context.Context, ipNet net.IPNet) {
	h.mu.Lock()
	defer h.mu.Unlock()

	cidr := ipNet.String()
	h.localHNAs[cidr] = ipNet

	h.eventBus.Publish(ctx, eventbus.Event{
		Type: eventbus.EventTopologyUpdate,
		Data: "local_hna_add",
	})
}

// RemoveLocalHNA removes an advertised network prefix.
func (h *HNAManager) RemoveLocalHNA(ctx context.Context, ipNet net.IPNet) {
	h.mu.Lock()
	defer h.mu.Unlock()

	cidr := ipNet.String()
	delete(h.localHNAs, cidr)

	h.eventBus.Publish(ctx, eventbus.Event{
		Type: eventbus.EventTopologyUpdate,
		Data: "local_hna_del",
	})
}

// GetLocalHNAs returns all local HNA network prefixes.
func (h *HNAManager) GetLocalHNAs() []net.IPNet {
	h.mu.RLock()
	defer h.mu.RUnlock()

	list := make([]net.IPNet, 0, len(h.localHNAs))
	for _, ipNet := range h.localHNAs {
		list = append(list, ipNet)
	}
	return list
}

// ProcessHNA processes incoming HNA messages from other gateway nodes.
func (h *HNAManager) ProcessHNA(ctx context.Context, originatorIP net.IP, hna HNAMessage, vtime time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()

	gwIP := originatorIP.String()
	now := time.Now()
	expTime := now.Add(vtime)
	updated := false

	for _, assoc := range hna.Associations {
		cidr := assoc.Netmask.String()
		if _, ok := h.hnaSet[cidr]; !ok {
			h.hnaSet[cidr] = make(map[string]*HNAEntry)
		}

		entry, ok := h.hnaSet[cidr][gwIP]
		if !ok {
			entry = &HNAEntry{
				GatewayIP: gwIP,
				Network:   assoc.Netmask,
				IsLocal:   false,
			}
			h.hnaSet[cidr][gwIP] = entry
			updated = true
		}

		entry.ExpirationTime = expTime
	}

	if updated {
		h.eventBus.Publish(ctx, eventbus.Event{
			Type: eventbus.EventTopologyUpdate,
			Data: "hna_recv",
		})
	}
}

// AgeOut removes expired gateway routes.
func (h *HNAManager) AgeOut(ctx context.Context) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	now := time.Now()
	changed := false

	for cidr, gateways := range h.hnaSet {
		for gw, entry := range gateways {
			if now.After(entry.ExpirationTime) {
				delete(gateways, gw)
				changed = true
			}
		}
		if len(gateways) == 0 {
			delete(h.hnaSet, cidr)
		}
	}

	if changed {
		h.eventBus.Publish(ctx, eventbus.Event{
			Type: eventbus.EventTopologyUpdate,
			Data: "hna_aging",
		})
	}
	return changed
}

// GetHNAEntries returns all active HNA entries (both local and remote gateways).
func (h *HNAManager) GetHNAEntries() []HNAEntry {
	h.mu.RLock()
	defer h.mu.RUnlock()

	list := make([]HNAEntry, 0)
	// Add local HNA entries
	for _, ipNet := range h.localHNAs {
		list = append(list, HNAEntry{
			GatewayIP: "local",
			Network:   ipNet,
			IsLocal:   true,
		})
	}

	// Add remote HNA entries
	for _, gateways := range h.hnaSet {
		for _, entry := range gateways {
			list = append(list, *entry)
		}
	}
	return list
}
