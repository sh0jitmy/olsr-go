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
	"container/heap"
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/shjtmy/frr-olsr/internal/eventbus"
)

type Route struct {
	Prefix      string // CIDR format, e.g., "10.0.0.2/32"
	NextHop     string // Next hop IP
	Metric      int
	IfaceIndex  int
}

// Priority Queue item for Dijkstra
type item struct {
	value    string // Node IP
	priority int    // Cost
	index    int
}

type priorityQueue []*item

func (pq priorityQueue) Len() int           { return len(pq) }
func (pq priorityQueue) Less(i, j int) bool { return pq[i].priority < pq[j].priority }
func (pq priorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index = i
	pq[j].index = j
}
func (pq *priorityQueue) Push(x interface{}) {
	n := len(*pq)
	it := x.(*item)
	it.index = n
	*pq = append(*pq, it)
}
func (pq *priorityQueue) Pop() interface{} {
	old := *pq
	n := len(old)
	it := old[n-1]
	old[n-1] = nil
	it.index = -1
	*pq = old[0 : n-1]
	return it
}

type SPFEngine struct {
	mu           sync.RWMutex
	routerID     nmLookup // Lookup interface or callback to identify local settings
	neighMgr     *NeighborManager
	topoMgr      *TopologyManager
	hnaMgr       *HNAManager
	eventBus     *eventbus.EventBus
	activeRoutes map[string]Route // Key: Prefix -> Route
}

type nmLookup interface {
	GetRouterID() string
	GetIfaceIndex(nextHop string) int
}

type LocalRouterLookup struct {
	RouterID         string
	IfaceIndexLookup func(ip string) int
}

func (l LocalRouterLookup) GetRouterID() string {
	return l.RouterID
}
func (l LocalRouterLookup) GetIfaceIndex(ip string) int {
	if l.IfaceIndexLookup != nil {
		return l.IfaceIndexLookup(ip)
	}
	return 0
}

func NewSPFEngine(lookup nmLookup, nm *NeighborManager, tm *TopologyManager, hm *HNAManager, bus *eventbus.EventBus) *SPFEngine {
	return &SPFEngine{
		routerID:     lookup,
		neighMgr:     nm,
		topoMgr:      tm,
		hnaMgr:       hm,
		eventBus:     bus,
		activeRoutes: make(map[string]Route),
	}
}

// CalculateRoutes executes Dijkstra's algorithm to resolve shortest paths
// and updates the active routing table. It publishes RouteInstall events for modifications.
func (s *SPFEngine) CalculateRoutes(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	startTime := time.Now()

	localID := s.routerID.GetRouterID()
	if localID == "" {
		return fmt.Errorf("local router ID is not set")
	}

	// 1. Gather all nodes and links to build adjacency list
	// Map of NodeIP -> list of neighbors (NextNodeIP -> cost)
	adj := make(map[string]map[string]int)

	addLink := func(u, v string, cost int) {
		if _, ok := adj[u]; !ok {
			adj[u] = make(map[string]int)
		}
		adj[u][v] = cost
	}

	// Direct 1-hop symmetric neighbors
	neighbors := s.neighMgr.GetNeighbors()
	for _, n := range neighbors {
		addLink(localID, n.NeighborIP, 1)
		// Assume symmetric links can go backwards too
		addLink(n.NeighborIP, localID, 1)
	}

	// 2-hop neighbors
	twoHops := s.neighMgr.GetTwoHopNeighbors()
	for oneHop, hops := range twoHops {
		for _, hop2 := range hops {
			addLink(oneHop, hop2, 1)
		}
	}

	// Topology database links (from TC)
	topoTuples := s.topoMgr.GetTopology()
	for _, t := range topoTuples {
		// destIP is reachable from lastIP (originator of TC)
		addLink(t.LastIP, t.DestIP, 1)
	}

	// 2. Run Dijkstra SPF
	dist := make(map[string]int)
	prev := make(map[string]string)
	visited := make(map[string]bool)

	pq := make(priorityQueue, 0)
	heap.Init(&pq)

	dist[localID] = 0
	heap.Push(&pq, &item{value: localID, priority: 0})

	for pq.Len() > 0 {
		u := heap.Pop(&pq).(*item).value
		if visited[u] {
			continue
		}
		visited[u] = true

		for v, cost := range adj[u] {
			alt := dist[u] + cost
			if d, ok := dist[v]; !ok || alt < d {
				dist[v] = alt
				prev[v] = u
				heap.Push(&pq, &item{value: v, priority: alt})
			}
		}
	}

	// 3. Resolve NextHops for each destination
	// For each node D, we backtrack using `prev` to find the first hop from localID
	newRoutes := make(map[string]Route)

	resolveNextHop := func(dest string) (string, int, bool) {
		curr := dest
		for {
			parent, ok := prev[curr]
			if !ok {
				return "", 0, false // Unreachable
			}
			if parent == localID {
				// curr is the first hop
				ifaceIdx := s.routerID.GetIfaceIndex(curr)
				return curr, ifaceIdx, true
			}
			curr = parent
		}
	}

	// Generate host routes (/32) for all reached nodes
	for nodeIP, d := range dist {
		if nodeIP == localID {
			continue
		}
		nextHop, ifaceIdx, ok := resolveNextHop(nodeIP)
		if ok {
			prefix := nodeIP + "/32"
			newRoutes[prefix] = Route{
				Prefix:     prefix,
				NextHop:    nextHop,
				Metric:     d,
				IfaceIndex: ifaceIdx,
			}
		}
	}

	// 4. Resolve Gateway HNA Routes
	// For each network advertised via HNA, install route to it via the best gateway
	hnaEntries := s.hnaMgr.GetHNAEntries()
	for _, entry := range hnaEntries {
		if entry.IsLocal {
			continue // Don't route to our own advertised external prefixes
		}
		gw := entry.GatewayIP
		// Get distance to gateway
		gwDist, reachable := dist[gw]
		if !reachable {
			continue // Gateway is currently unreachable
		}

		nextHop, ifaceIdx, ok := resolveNextHop(gw)
		if ok {
			prefix := entry.Network.String()
			
			// If route already exists (e.g. from a different gateway), keep the one with shorter path
			if existing, ok := newRoutes[prefix]; !ok || gwDist < existing.Metric {
				newRoutes[prefix] = Route{
					Prefix:     prefix,
					NextHop:    nextHop,
					Metric:     gwDist + 1, // Gateway cost + 1 to reach external network
					IfaceIndex: ifaceIdx,
				}
			}
		}
	}

	// 5. Diff and publish route installs/deletions
	for prefix, oldRoute := range s.activeRoutes {
		newRoute, exists := newRoutes[prefix]
		if !exists {
			// Route withdrawn
			s.eventBus.Publish(ctx, eventbus.Event{
				Type: eventbus.EventRouteInstall,
				Data: map[string]interface{}{
					"action": "delete",
					"route":  oldRoute,
				},
			})
		} else if oldRoute.NextHop != newRoute.NextHop || oldRoute.IfaceIndex != newRoute.IfaceIndex {
			// Route updated
			s.eventBus.Publish(ctx, eventbus.Event{
				Type: eventbus.EventRouteInstall,
				Data: map[string]interface{}{
					"action": "add", // Add works as update in Zebra
					"route":  newRoute,
				},
			})
		}
	}

	// Brand new routes
	for prefix, newRoute := range newRoutes {
		if _, exists := s.activeRoutes[prefix]; !exists {
			s.eventBus.Publish(ctx, eventbus.Event{
				Type: eventbus.EventRouteInstall,
				Data: map[string]interface{}{
					"action": "add",
					"route":  newRoute,
				},
			})
		}
	}

	s.activeRoutes = newRoutes

	// Record SPF run metrics (duration, runs)
	spfDuration := time.Since(startTime).Seconds()
	s.eventBus.Publish(ctx, eventbus.Event{
		Type: eventbus.EventSPFTrigger,
		Data: map[string]interface{}{
			"duration":    spfDuration,
			"route_count": len(newRoutes),
		},
	})

	return nil
}

// GetRoutes returns the active unicast routes
func (s *SPFEngine) GetRoutes() []Route {
	s.mu.RLock()
	defer s.mu.RUnlock()

	list := make([]Route, 0, len(s.activeRoutes))
	for _, r := range s.activeRoutes {
		list = append(list, r)
	}
	return list
}

// GetNextHopForDest returns the next hop and interface index for a destination node IP.
// Used by MOLSR to determine the parent direction.
func (s *SPFEngine) GetNextHopForDest(dest string) (string, int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Check direct /32 host route
	prefix := dest + "/32"
	r, ok := s.activeRoutes[prefix]
	if ok {
		return r.NextHop, r.IfaceIndex, nil
	}

	// Fallback to check if destination is covered by any network prefix (longest prefix match)
	destIP := net.ParseIP(dest)
	if destIP == nil {
		return "", 0, fmt.Errorf("invalid destination IP format: %s", dest)
	}

	var bestRoute Route
	bestMaskLen := -1

	for _, r := range s.activeRoutes {
		_, ipNet, err := net.ParseCIDR(r.Prefix)
		if err != nil {
			continue
		}
		if ipNet.Contains(destIP) {
			maskLen, _ := ipNet.Mask.Size()
			if maskLen > bestMaskLen {
				bestMaskLen = maskLen
				bestRoute = r
			}
		}
	}

	if bestMaskLen != -1 {
		return bestRoute.NextHop, bestRoute.IfaceIndex, nil
	}

	return "", 0, fmt.Errorf("route to destination %s not found", dest)
}
