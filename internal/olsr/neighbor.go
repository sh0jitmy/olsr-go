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

type LinkState int

const (
	LinkStatePending LinkState = iota
	LinkStateAsymmetric
	LinkStateSymmetric
	LinkStateLost
)

func (s LinkState) String() string {
	switch s {
	case LinkStatePending:
		return "Pending"
	case LinkStateAsymmetric:
		return "Asymmetric"
	case LinkStateSymmetric:
		return "Symmetric"
	case LinkStateLost:
		return "Lost"
	default:
		return "Unknown"
	}
}

type LinkTuple struct {
	LocalIP        string
	NeighborIP     string
	State          LinkState
	AsymTime       time.Time
	SymTime        time.Time
	ExpirationTime time.Time
}

type NeighborTuple struct {
	NeighborIP  string
	Symmetric   bool
	Willingness uint8
}

type TwoHopTuple struct {
	NeighborIP     string // 1-hop neighbor
	TwoHopIP       string // 2-hop neighbor
	ExpirationTime time.Time
}

type NeighborManager struct {
	mu           sync.RWMutex
	routerID     string
	links        map[string]*LinkTuple        // Key: neighbor IP
	neighbors    map[string]*NeighborTuple    // Key: neighbor IP
	twoHopLinks  map[string]map[string]time.Time // Key: 1-hop IP -> 2-hop IP -> ExpirationTime
	mprSet       map[string]bool              // Key: neighbor IP
	mprSelectors map[string]time.Time         // Key: selector IP -> ExpirationTime
	eventBus     *eventbus.EventBus
}

func NewNeighborManager(routerID string, bus *eventbus.EventBus) *NeighborManager {
	return &NeighborManager{
		routerID:     routerID,
		links:        make(map[string]*LinkTuple),
		neighbors:    make(map[string]*NeighborTuple),
		twoHopLinks:  make(map[string]map[string]time.Time),
		mprSet:       make(map[string]bool),
		mprSelectors: make(map[string]time.Time),
		eventBus:     bus,
	}
}

// ProcessHello updates the link state, neighbor set, 2-hop neighbor set, and recalculates MPRs.
func (nm *NeighborManager) ProcessHello(ctx context.Context, senderIP net.IP, originatorIP net.IP, hello HelloMessage, vtime time.Duration) {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	senderStr := senderIP.String()
	origStr := originatorIP.String()
	now := time.Now()
	expTime := now.Add(vtime)

	// 1. Update Link State
	link, exists := nm.links[senderStr]
	if !exists {
		link = &LinkTuple{
			LocalIP:    nm.routerID,
			NeighborIP: senderStr,
			State:      LinkStateAsymmetric,
		}
		nm.links[senderStr] = link
	}

	// Check if this node is listed in the HELLO message
	isLocalSymmetric := false
	for _, lm := range hello.LinkMessages {
		linkType := lm.LinkCode & 0x03
		neighType := (lm.LinkCode >> 2) & 0x03

		for _, addr := range lm.NeighborAddresses {
			if addr.String() == nm.routerID {
				if linkType == LinkTypeSym || linkType == LinkTypeAsym || neighType == NeighTypeSym || neighType == NeighTypeMPR {
					isLocalSymmetric = true
				}
			}
		}
	}

	oldState := link.State
	if isLocalSymmetric {
		link.State = LinkStateSymmetric
		link.SymTime = expTime
		link.ExpirationTime = expTime
	} else {
		link.State = LinkStateAsymmetric
		link.AsymTime = expTime
		link.ExpirationTime = expTime
	}

	// 2. Update Neighbor Tuple
	neigh, exists := nm.neighbors[origStr]
	if !exists {
		neigh = &NeighborTuple{
			NeighborIP: origStr,
		}
		nm.neighbors[origStr] = neigh
	}
	neigh.Willingness = hello.Willingness
	neigh.Symmetric = (link.State == LinkStateSymmetric)

	// Handle MPR Selector Set: If the sender selected us as MPR
	isMPRSelector := false
	for _, lm := range hello.LinkMessages {
		neighType := (lm.LinkCode >> 2) & 0x03
		if neighType == NeighTypeMPR {
			for _, addr := range lm.NeighborAddresses {
				if addr.String() == nm.routerID {
					isMPRSelector = true
					break
				}
			}
		}
	}
	if isMPRSelector && neigh.Symmetric {
		nm.mprSelectors[origStr] = expTime
	}

	// 3. Update 2-Hop Neighbors
	if neigh.Symmetric {
		// Clear previous 2-hop list for this sender to rebuild
		nm.twoHopLinks[origStr] = make(map[string]time.Time)

		for _, lm := range hello.LinkMessages {
			neighType := (lm.LinkCode >> 2) & 0x03
			// Only learn from symmetric or MPR neighbors of the sender
			if neighType == NeighTypeSym || neighType == NeighTypeMPR {
				for _, addr := range lm.NeighborAddresses {
					addrStr := addr.String()
					// Exclude ourselves and direct 1-hop neighbor itself
					if addrStr != nm.routerID && addrStr != origStr {
						nm.twoHopLinks[origStr][addrStr] = expTime
					}
				}
			}
		}
	} else {
		// If neighbor is no longer symmetric, clear 2-hop links
		delete(nm.twoHopLinks, origStr)
	}

	// 4. Recalculate MPR Set if Link/Neighbor state changed
	stateChanged := (oldState != link.State)
	if stateChanged {
		nm.recalculateMPRs(ctx)
		// Publish event
		nm.eventBus.Publish(ctx, eventbus.Event{
			Type: eventbus.EventNeighborUpdate,
			Data: origStr,
		})
	}
}

// Recalculate MPRs based on RFC 3626 Section 8.3
func (nm *NeighborManager) recalculateMPRs(ctx context.Context) {
	newMpr := make(map[string]bool)

	// N = set of symmetric 1-hop neighbors
	// N2 = set of symmetric 2-hop neighbors
	// We want to select subset of N to cover N2.
	symmetric1Hop := make(map[string]*NeighborTuple)
	for ip, n := range nm.neighbors {
		if n.Symmetric && n.Willingness > WillNever {
			symmetric1Hop[ip] = n
		}
	}

	// Collect 2-hop neighbors that are not in N and are not ourselves
	twoHopNeighbors := make(map[string]map[string]bool) // Key: 2-hop IP -> Set of 1-hop IPs that reach it
	for oneHopIP, twoHops := range nm.twoHopLinks {
		// Verify if the 1-hop node is symmetric
		if n, ok := symmetric1Hop[oneHopIP]; !ok || !n.Symmetric {
			continue
		}
		for twoHopIP, exp := range twoHops {
			if time.Now().After(exp) {
				continue
			}
			// Exclude if it is a 1-hop symmetric neighbor
			if _, is1Hop := symmetric1Hop[twoHopIP]; is1Hop {
				continue
			}
			if _, ok := twoHopNeighbors[twoHopIP]; !ok {
				twoHopNeighbors[twoHopIP] = make(map[string]bool)
			}
			twoHopNeighbors[twoHopIP][oneHopIP] = true
		}
	}

	// 1. Select 1-hop nodes that are the ONLY way to reach some 2-hop nodes
	for _, oneHops := range twoHopNeighbors {
		if len(oneHops) == 1 {
			for oneHopIP := range oneHops {
				newMpr[oneHopIP] = true
			}
		}
	}

	// Remove covered 2-hop neighbors from consideration
	for {
		uncovered := make(map[string]map[string]bool)
		for twoHopIP, oneHops := range twoHopNeighbors {
			covered := false
			for mprIP := range newMpr {
				if oneHops[mprIP] {
					covered = true
					break
				}
			}
			if !covered {
				uncovered[twoHopIP] = oneHops
			}
		}

		if len(uncovered) == 0 {
			break
		}

		// 2. Select the 1-hop node that covers the maximum number of remaining uncovered 2-hop nodes.
		// If ties, choose the one with higher Willingness.
		// If still ties, choose the one with higher 1-hop neighbor count.
		var bestOneHop string
		bestCoverCount := -1
		bestWill := uint8(0)

		for oneHopIP, neigh := range symmetric1Hop {
			// Count how many uncovered 2-hop nodes this 1-hop covers
			coverCount := 0
			for _, oneHops := range uncovered {
				if oneHops[oneHopIP] {
					coverCount++
				}
			}

			if coverCount == 0 {
				continue
			}

			if coverCount > bestCoverCount {
				bestCoverCount = coverCount
				bestOneHop = oneHopIP
				bestWill = neigh.Willingness
			} else if coverCount == bestCoverCount {
				// Tie breaking
				if neigh.Willingness > bestWill {
					bestOneHop = oneHopIP
					bestWill = neigh.Willingness
				} else if neigh.Willingness == bestWill {
					// Compare degrees (total number of 2-hop links it provides)
					currentDegree := len(nm.twoHopLinks[oneHopIP])
					bestDegree := len(nm.twoHopLinks[bestOneHop])
					if currentDegree > bestDegree {
						bestOneHop = oneHopIP
					}
				}
			}
		}

		if bestOneHop == "" {
			// No more coverage possible
			break
		}

		newMpr[bestOneHop] = true
	}

	nm.mprSet = newMpr
}

// AgeOut ages out expired links, neighbors, 2-hop tuples, and MPR selector states.
func (nm *NeighborManager) AgeOut(ctx context.Context) bool {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	now := time.Now()
	changed := false

	// Age links
	for ip, link := range nm.links {
		if now.After(link.ExpirationTime) {
			if link.State != LinkStateLost {
				link.State = LinkStateLost
				changed = true
				// Trigger neighbor state symmetry update
				if neigh, ok := nm.neighbors[ip]; ok {
					neigh.Symmetric = false
				}
				delete(nm.twoHopLinks, ip)
			}
		}
	}

	// Age 2-hop links
	for oneHopIP, twoHops := range nm.twoHopLinks {
		for twoHopIP, exp := range twoHops {
			if now.After(exp) {
				delete(twoHops, twoHopIP)
				changed = true
			}
		}
		if len(twoHops) == 0 {
			delete(nm.twoHopLinks, oneHopIP)
		}
	}

	// Age MPR Selector Set
	for ip, exp := range nm.mprSelectors {
		if now.After(exp) {
			delete(nm.mprSelectors, ip)
			changed = true
		}
	}

	if changed {
		nm.recalculateMPRs(ctx)
		nm.eventBus.Publish(ctx, eventbus.Event{
			Type: eventbus.EventNeighborUpdate,
			Data: "aging",
		})
	}

	return changed
}

// GetNeighbors returns a list of active neighbors
func (nm *NeighborManager) GetNeighbors() []NeighborTuple {
	nm.mu.RLock()
	defer nm.mu.RUnlock()

	list := make([]NeighborTuple, 0, len(nm.neighbors))
	for _, n := range nm.neighbors {
		// Include if the corresponding link is not lost/expired
		if l, ok := nm.links[n.NeighborIP]; ok && l.State != LinkStateLost {
			list = append(list, *n)
		}
	}
	return list
}

// GetLinks returns current link states
func (nm *NeighborManager) GetLinks() []LinkTuple {
	nm.mu.RLock()
	defer nm.mu.RUnlock()

	list := make([]LinkTuple, 0, len(nm.links))
	for _, l := range nm.links {
		list = append(list, *l)
	}
	return list
}

// GetMPRSet returns the active MPR set
func (nm *NeighborManager) GetMPRSet() []string {
	nm.mu.RLock()
	defer nm.mu.RUnlock()

	list := make([]string, 0, len(nm.mprSet))
	for ip := range nm.mprSet {
		list = append(list, ip)
	}
	return list
}

// GetMPRSelectors returns selectors that chose this node as MPR
func (nm *NeighborManager) GetMPRSelectors() []string {
	nm.mu.RLock()
	defer nm.mu.RUnlock()

	now := time.Now()
	list := make([]string, 0)
	for ip, exp := range nm.mprSelectors {
		if now.Before(exp) {
			list = append(list, ip)
		}
	}
	return list
}

// GetTwoHopNeighbors returns a list of 2-hop neighbors grouped by the 1-hop neighbor
func (nm *NeighborManager) GetTwoHopNeighbors() map[string][]string {
	nm.mu.RLock()
	defer nm.mu.RUnlock()

	result := make(map[string][]string)
	now := time.Now()
	for oneHop, twoHops := range nm.twoHopLinks {
		list := make([]string, 0)
		for twoHop, exp := range twoHops {
			if now.Before(exp) {
				list = append(list, twoHop)
			}
		}
		if len(list) > 0 {
			result[oneHop] = list
		}
	}
	return result
}
