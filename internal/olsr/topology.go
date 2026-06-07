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

	"github.com/sh0jitmy/olsr-go/internal/eventbus"
)

type TopologyTuple struct {
	DestIP         string
	LastIP         string
	SeqNum         uint16
	ExpirationTime time.Time
}

type DuplicateTuple struct {
	OriginatorIP   string
	SeqNum         uint16
	ExpirationTime time.Time
}

type TopologyManager struct {
	mu            sync.RWMutex
	topology      map[string]map[string]*TopologyTuple // Key: destIP -> lastIP -> Tuple
	duplicates    map[string]map[uint16]time.Time      // Key: originatorIP -> seqNum -> ExpirationTime
	midMap        map[string][]string                  // Key: primaryIP -> aliasIPs
	reverseMidMap map[string]string                    // Key: aliasIP -> primaryIP
	eventBus      *eventbus.EventBus
}

func NewTopologyManager(bus *eventbus.EventBus) *TopologyManager {
	return &TopologyManager{
		topology:      make(map[string]map[string]*TopologyTuple),
		duplicates:    make(map[string]map[uint16]time.Time),
		midMap:        make(map[string][]string),
		reverseMidMap: make(map[string]string),
		eventBus:      bus,
	}
}

// IsDuplicate checks and records if the message has already been received.
// Return true if duplicate (should be suppressed).
func (tm *TopologyManager) IsDuplicate(originator net.IP, seqNum uint16, vtime time.Duration) bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	origStr := originator.String()
	now := time.Now()

	if _, ok := tm.duplicates[origStr]; !ok {
		tm.duplicates[origStr] = make(map[uint16]time.Time)
	}

	exp, ok := tm.duplicates[origStr][seqNum]
	if ok && now.Before(exp) {
		return true // Duplicate detected
	}

	// Register duplicate tuple
	tm.duplicates[origStr][seqNum] = now.Add(vtime)
	return false
}

// ProcessTC updates topology database using entries from received TC messages.
func (tm *TopologyManager) ProcessTC(ctx context.Context, originatorIP net.IP, tc TCMessage, vtime time.Duration) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	lastIP := originatorIP.String()
	now := time.Now()
	expTime := now.Add(vtime)
	updated := false

	// For each advertised neighbor (destination) in TC
	for _, addr := range tc.NeighborAddresses {
		destIP := addr.String()

		if _, ok := tm.topology[destIP]; !ok {
			tm.topology[destIP] = make(map[string]*TopologyTuple)
		}

		tuple, ok := tm.topology[destIP][lastIP]
		if !ok {
			tuple = &TopologyTuple{
				DestIP: destIP,
				LastIP: lastIP,
			}
			tm.topology[destIP][lastIP] = tuple
			updated = true
		}

		// Update sequence number and expiration
		// Check if sequence number is newer (or wrap-around handled)
		if !ok || tc.ANSN >= tuple.SeqNum || (tuple.SeqNum-tc.ANSN > 32768) {
			tuple.SeqNum = tc.ANSN
			tuple.ExpirationTime = expTime
			updated = true
		}
	}

	if updated {
		tm.eventBus.Publish(ctx, eventbus.Event{
			Type: eventbus.EventTopologyUpdate,
			Data: lastIP,
		})
	}
}

// ProcessMID registers Multiple Interface Declarations.
func (tm *TopologyManager) ProcessMID(ctx context.Context, originatorIP net.IP, mid MIDMessage, vtime time.Duration) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	primary := originatorIP.String()
	// Clear previous mappings for this primary
	if oldAliases, ok := tm.midMap[primary]; ok {
		for _, alias := range oldAliases {
			delete(tm.reverseMidMap, alias)
		}
	}

	aliases := make([]string, len(mid.Addresses))
	for i, addr := range mid.Addresses {
		alias := addr.String()
		aliases[i] = alias
		tm.reverseMidMap[alias] = primary
	}
	tm.midMap[primary] = aliases

	tm.eventBus.Publish(ctx, eventbus.Event{
		Type: eventbus.EventTopologyUpdate,
		Data: "mid",
	})
}

// GetPrimaryAddress returns the primary IP address if the given IP is an alias registered in MID.
// Otherwise, returns the IP itself.
func (tm *TopologyManager) GetPrimaryAddress(ip string) string {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	if primary, ok := tm.reverseMidMap[ip]; ok {
		return primary
	}
	return ip
}

// AgeOut removes expired topology and duplicate records.
func (tm *TopologyManager) AgeOut(ctx context.Context) bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	now := time.Now()
	changed := false

	// Age topology tuples
	for dest, lasts := range tm.topology {
		for last, tuple := range lasts {
			if now.After(tuple.ExpirationTime) {
				delete(lasts, last)
				changed = true
			}
		}
		if len(lasts) == 0 {
			delete(tm.topology, dest)
		}
	}

	// Age duplicate entries
	for orig, seqs := range tm.duplicates {
		for seq, exp := range seqs {
			if now.After(exp) {
				delete(seqs, seq)
			}
		}
		if len(seqs) == 0 {
			delete(tm.duplicates, orig)
		}
	}

	if changed {
		tm.eventBus.Publish(ctx, eventbus.Event{
			Type: eventbus.EventTopologyUpdate,
			Data: "aging",
		})
	}

	return changed
}

// GetTopology returns a flat list of current topology tuples
func (tm *TopologyManager) GetTopology() []TopologyTuple {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	list := make([]TopologyTuple, 0)
	for _, lasts := range tm.topology {
		for _, tuple := range lasts {
			list = append(list, *tuple)
		}
	}
	return list
}

// GetMIDMap returns a copy of the MID map
func (tm *TopologyManager) GetMIDMap() map[string][]string {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	copyMap := make(map[string][]string)
	for k, v := range tm.midMap {
		copiedSlice := make([]string, len(v))
		copy(copiedSlice, v)
		copyMap[k] = copiedSlice
	}
	return copyMap
}
