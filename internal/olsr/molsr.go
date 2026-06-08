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

type SourceClaim struct {
	SourceIP       string
	GroupID        string
	OriginatorIP   string
	ExpirationTime time.Time
}

type ConfirmParentEntry struct {
	SourceIP       string
	GroupID        string
	ParentIP       string
	ChildIP        string
	ExpirationTime time.Time
}

// MulticastForwardingEntry represents an active kernel multicast forwarding state (MFC)
type MulticastForwardingEntry struct {
	SourceIP string
	GroupID  string
	IIF      int   // Incoming Interface Index
	OIFs     []int // Outgoing Interface Indices
}

type MOLSRManager struct {
	mu             sync.RWMutex
	routerID       string
	sourceClaims   map[string]map[string]*SourceClaim                   // Key: GroupIP -> SourceIP -> Claim
	confirmParents map[string]map[string]map[string]*ConfirmParentEntry // Key: GroupIP -> SourceIP -> ChildIP -> Entry
	localGroup     map[string]bool                                      // Groups this router has locally joined
	activeMfc      map[string]*MulticastForwardingEntry                 // Key: "Source:Group" -> Forwarding state
	eventBus       *eventbus.EventBus

	// Lookup function to find the interface index toward a next-hop IP
	IfaceIndexLookup func(nextHopIP string) (int, error)
}

func NewMOLSRManager(routerID string, bus *eventbus.EventBus) *MOLSRManager {
	return &MOLSRManager{
		routerID:       routerID,
		sourceClaims:   make(map[string]map[string]*SourceClaim),
		confirmParents: make(map[string]map[string]map[string]*ConfirmParentEntry),
		localGroup:     make(map[string]bool),
		activeMfc:      make(map[string]*MulticastForwardingEntry),
		eventBus:       bus,
	}
}

// ProcessSourceClaim registers an incoming SOURCE CLAIM message.
func (m *MOLSRManager) ProcessSourceClaim(ctx context.Context, originatorIP net.IP, sc SourceClaimMessage, vtime time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	grp := sc.GroupID.String()
	src := sc.SourceIP.String()
	orig := originatorIP.String()
	now := time.Now()

	if _, ok := m.sourceClaims[grp]; !ok {
		m.sourceClaims[grp] = make(map[string]*SourceClaim)
	}

	claim, ok := m.sourceClaims[grp][src]
	if !ok {
		claim = &SourceClaim{
			SourceIP:     src,
			GroupID:      grp,
			OriginatorIP: orig,
		}
		m.sourceClaims[grp][src] = claim
	}
	claim.ExpirationTime = now.Add(vtime)

	m.eventBus.Publish(ctx, eventbus.Event{
		Type: eventbus.EventTopologyUpdate,
		Data: "source_claim_recv",
	})
}

// ProcessConfirmParent registers an incoming CONFIRM PARENT message designating a parent.
func (m *MOLSRManager) ProcessConfirmParent(ctx context.Context, originatorIP net.IP, cp ConfirmParentMessage, vtime time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	grp := cp.GroupID.String()
	src := cp.SourceIP.String()
	parent := cp.ParentIP.String()
	child := originatorIP.String()
	now := time.Now()

	// Only process if designated parent is this router
	if parent != m.routerID {
		return
	}

	if _, ok := m.confirmParents[grp]; !ok {
		m.confirmParents[grp] = make(map[string]map[string]*ConfirmParentEntry)
	}
	if _, ok := m.confirmParents[grp][src]; !ok {
		m.confirmParents[grp][src] = make(map[string]*ConfirmParentEntry)
	}

	entry, ok := m.confirmParents[grp][src][child]
	if !ok {
		entry = &ConfirmParentEntry{
			SourceIP: src,
			GroupID:  grp,
			ParentIP: parent,
			ChildIP:  child,
		}
		m.confirmParents[grp][src][child] = entry
	}
	entry.ExpirationTime = now.Add(vtime)

	m.eventBus.Publish(ctx, eventbus.Event{
		Type: eventbus.EventTopologyUpdate,
		Data: "confirm_parent_recv",
	})
}

// JoinGroup registers the local router as a member of a multicast group.
func (m *MOLSRManager) JoinGroup(ctx context.Context, groupIP string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.localGroup[groupIP] = true
}

// LeaveGroup unregisters the local router from a multicast group.
func (m *MOLSRManager) LeaveGroup(ctx context.Context, groupIP string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.localGroup, groupIP)
}

// AddLocalSourceClaim adds a SourceClaim manually (useful for REST API or local sources)
func (m *MOLSRManager) AddLocalSourceClaim(ctx context.Context, srcIP, grpIP string, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.sourceClaims[grpIP]; !ok {
		m.sourceClaims[grpIP] = make(map[string]*SourceClaim)
	}
	m.sourceClaims[grpIP][srcIP] = &SourceClaim{
		SourceIP:       srcIP,
		GroupID:        grpIP,
		OriginatorIP:   m.routerID,
		ExpirationTime: time.Now().Add(duration),
	}

	m.eventBus.Publish(ctx, eventbus.Event{
		Type: eventbus.EventTopologyUpdate,
		Data: "local_source_claim_add",
	})
}

// DeleteLocalSourceClaim removes a SourceClaim
func (m *MOLSRManager) DeleteLocalSourceClaim(ctx context.Context, srcIP, grpIP string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if grps, ok := m.sourceClaims[grpIP]; ok {
		delete(grps, srcIP)
	}

	m.eventBus.Publish(ctx, eventbus.Event{
		Type: eventbus.EventTopologyUpdate,
		Data: "local_source_claim_del",
	})
}

// AddLocalConfirmParent adds a ConfirmParent entry manually (useful for REST API)
func (m *MOLSRManager) AddLocalConfirmParent(ctx context.Context, srcIP, grpIP, parentIP, childIP string, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.confirmParents[grpIP]; !ok {
		m.confirmParents[grpIP] = make(map[string]map[string]*ConfirmParentEntry)
	}
	if _, ok := m.confirmParents[grpIP][srcIP]; !ok {
		m.confirmParents[grpIP][srcIP] = make(map[string]*ConfirmParentEntry)
	}
	m.confirmParents[grpIP][srcIP][childIP] = &ConfirmParentEntry{
		SourceIP:       srcIP,
		GroupID:        grpIP,
		ParentIP:       parentIP,
		ChildIP:        childIP,
		ExpirationTime: time.Now().Add(duration),
	}

	m.eventBus.Publish(ctx, eventbus.Event{
		Type: eventbus.EventTopologyUpdate,
		Data: "local_confirm_parent_add",
	})
}

// DeleteLocalConfirmParent removes a ConfirmParent entry
func (m *MOLSRManager) DeleteLocalConfirmParent(ctx context.Context, srcIP, grpIP, childIP string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if grps, ok := m.confirmParents[grpIP]; ok {
		if srcs, ok := grps[srcIP]; ok {
			delete(srcs, childIP)
		}
	}

	m.eventBus.Publish(ctx, eventbus.Event{
		Type: eventbus.EventTopologyUpdate,
		Data: "local_confirm_parent_del",
	})
}

// AgeOut ages out expired SourceClaims and ConfirmParents.
func (m *MOLSRManager) AgeOut(ctx context.Context) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	changed := false

	// Age Source Claims
	for grp, sources := range m.sourceClaims {
		for src, claim := range sources {
			if now.After(claim.ExpirationTime) {
				delete(sources, src)
				changed = true
			}
		}
		if len(sources) == 0 {
			delete(m.sourceClaims, grp)
		}
	}

	// Age Confirm Parents
	for grp, sources := range m.confirmParents {
		for src, children := range sources {
			for child, entry := range children {
				if now.After(entry.ExpirationTime) {
					delete(children, child)
					changed = true
				}
			}
			if len(children) == 0 {
				delete(sources, src)
			}
		}
		if len(sources) == 0 {
			delete(m.confirmParents, grp)
		}
	}

	if changed {
		m.eventBus.Publish(ctx, eventbus.Event{
			Type: eventbus.EventTopologyUpdate,
			Data: "molsr_aging",
		})
	}

	return changed
}

// RecalculateMroutes builds the multicast forwarding states based on ConfirmParents and Unicast SPF.
// It generates event notifications to install/withdraw IPMR routes.
func (m *MOLSRManager) RecalculateMroutes(ctx context.Context, getUnicastNextHop func(dest string) (string, int, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.IfaceIndexLookup == nil {
		return // Cannot resolve interfaces
	}

	newMfc := m.buildNewMFC(getUnicastNextHop)
	m.syncMFCRoutes(ctx, newMfc)
	m.activeMfc = newMfc
}

func (m *MOLSRManager) buildNewMFC(getUnicastNextHop func(dest string) (string, int, error)) map[string]*MulticastForwardingEntry {
	newMfc := make(map[string]*MulticastForwardingEntry)

	// Traverse confirm parents. If we received CONFIRM PARENT designating us, we must forward
	for grp, sources := range m.confirmParents {
		for src, children := range sources {
			if len(children) == 0 {
				continue
			}

			// We are the parent. Resolve IIF toward Source (via Unicast SPF next hop)
			_, srcIfaceIndex, err := getUnicastNextHop(src)
			if err != nil {
				// No route to source, cannot forward
				continue
			}

			// Resolve Outgoing Interfaces (OIFs) toward each child node
			oifs := make([]int, 0)
			oifSeen := make(map[int]bool)
			for child := range children {
				// Find next-hop interface toward this child (via Unicast SPF)
				_, childIfaceIndex, err := getUnicastNextHop(child)
				if err == nil {
					// Add interface toward child to OIF if not already present
					if !oifSeen[childIfaceIndex] && childIfaceIndex != srcIfaceIndex {
						oifs = append(oifs, childIfaceIndex)
						oifSeen[childIfaceIndex] = true
					}
				}
			}

			if len(oifs) > 0 || len(children) > 0 {
				key := src + ":" + grp
				newMfc[key] = &MulticastForwardingEntry{
					SourceIP: src,
					GroupID:  grp,
					IIF:      srcIfaceIndex,
					OIFs:     oifs,
				}
			}
		}
	}
	return newMfc
}

func (m *MOLSRManager) syncMFCRoutes(ctx context.Context, newMfc map[string]*MulticastForwardingEntry) {
	// Determine changes between old active MFC and new MFC to trigger ZAPI router updates
	for key, oldEntry := range m.activeMfc {
		newEntry, exists := newMfc[key]
		if !exists {
			// Route withdrawn
			m.eventBus.Publish(ctx, eventbus.Event{
				Type: eventbus.EventRouteInstall,
				Data: map[string]interface{}{
					"action": "delete_multicast",
					"entry":  oldEntry,
				},
			})
		} else {
			// Compare IIF and OIFs
			oifsChanged := len(oldEntry.OIFs) != len(newEntry.OIFs)
			if !oifsChanged {
				for i, v := range oldEntry.OIFs {
					if newEntry.OIFs[i] != v {
						oifsChanged = true
						break
					}
				}
			}
			if oldEntry.IIF != newEntry.IIF || oifsChanged {
				// Update route
				m.eventBus.Publish(ctx, eventbus.Event{
					Type: eventbus.EventRouteInstall,
					Data: map[string]interface{}{
						"action": "add_multicast",
						"entry":  newEntry,
					},
				})
			}
		}
	}

	// Brand new entries
	for key, newEntry := range newMfc {
		if _, exists := m.activeMfc[key]; !exists {
			m.eventBus.Publish(ctx, eventbus.Event{
				Type: eventbus.EventRouteInstall,
				Data: map[string]interface{}{
					"action": "add_multicast",
					"entry":  newEntry,
				},
			})
		}
	}
}

// GetSourceClaims returns all active source claims
func (m *MOLSRManager) GetSourceClaims() []SourceClaim {
	m.mu.RLock()
	defer m.mu.RUnlock()

	list := make([]SourceClaim, 0)
	for _, sources := range m.sourceClaims {
		for _, claim := range sources {
			list = append(list, *claim)
		}
	}
	return list
}

// GetConfirmParents returns all active confirm parent tuples
func (m *MOLSRManager) GetConfirmParents() []ConfirmParentEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	list := make([]ConfirmParentEntry, 0)
	for _, sources := range m.confirmParents {
		for _, children := range sources {
			for _, entry := range children {
				list = append(list, *entry)
			}
		}
	}
	return list
}

// GetActiveMFC returns copy of active forwarding table
func (m *MOLSRManager) GetActiveMFC() []MulticastForwardingEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	list := make([]MulticastForwardingEntry, 0, len(m.activeMfc))
	for _, entry := range m.activeMfc {
		list = append(list, *entry)
	}
	return list
}
