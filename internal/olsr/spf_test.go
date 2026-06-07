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
	"testing"
	"time"

	"github.com/shjtmy/frr-olsr/internal/eventbus"
)

func TestSPFRouteCalculation(t *testing.T) {
	bus := eventbus.NewEventBus(10, 10*time.Millisecond)
	nm := NewNeighborManager("1.1.1.1", bus)
	tm := NewTopologyManager(bus)
	hm := NewHNAManager(bus)
	ctx := context.Background()

	// Mock RouterID and Interface Index Lookup
	lookup := LocalRouterLookup{
		RouterID: "1.1.1.1",
		IfaceIndexLookup: func(ip string) int {
			if ip == "2.2.2.2" {
				return 10 // interface eth1
			}
			if ip == "3.3.3.3" {
				return 20 // interface eth2
			}
			return 0
		},
	}

	spf := NewSPFEngine(lookup, nm, tm, hm, bus)

	// Subscribe to route events
	sub := bus.Subscribe(eventbus.EventRouteInstall)
	defer bus.Unsubscribe(sub)

	// 1. Setup direct symmetric neighbor 2.2.2.2
	hello2 := HelloMessage{
		Htime:       2 * time.Second,
		Willingness: WillDefault,
		LinkMessages: []HelloLinkMessage{
			{
				LinkCode:          (LinkTypeSym & 0x03) | ((NeighTypeSym & 0x03) << 2),
				NeighborAddresses: []net.IP{net.ParseIP("1.1.1.1"), net.ParseIP("4.4.4.4")},
			},
		},
	}
	nm.ProcessHello(ctx, net.ParseIP("2.2.2.2"), net.ParseIP("2.2.2.2"), hello2, 10*time.Second)

	// 2. Setup topology link: 4.4.4.4 -> 5.5.5.5
	tm.ProcessTC(ctx, net.ParseIP("4.4.4.4"), TCMessage{
		ANSN:              1,
		NeighborAddresses: []net.IP{net.ParseIP("5.5.5.5")},
	}, 10*time.Second)

	// 3. Setup HNA entry: 5.5.5.5 advertises external subnet 192.168.100.0/24
	_, ipNet, _ := net.ParseCIDR("192.168.100.0/24")
	hm.ProcessHNA(ctx, net.ParseIP("5.5.5.5"), HNAMessage{
		Associations: []HNAAssociation{
			{
				Address: ipNet.IP,
				Netmask: *ipNet,
			},
		},
	}, 10*time.Second)

	// Trigger calculations
	err := spf.CalculateRoutes(ctx)
	if err != nil {
		t.Fatalf("failed SPF: %v", err)
	}

	routes := spf.GetRoutes()
	// Should have routes to:
	// - 2.2.2.2/32 (metric 1, next hop 2.2.2.2, iface 10)
	// - 4.4.4.4/32 (metric 2, next hop 2.2.2.2, iface 10)
	// - 5.5.5.5/32 (metric 3, next hop 2.2.2.2, iface 10)
	// - 192.168.100.0/24 (metric 4, next hop 2.2.2.2, iface 10)
	
	routeMap := make(map[string]Route)
	for _, r := range routes {
		routeMap[r.Prefix] = r
	}

	if len(routeMap) != 4 {
		t.Errorf("expected 4 active routes, got %d: %+v", len(routeMap), routeMap)
	}

	r2, exists := routeMap["2.2.2.2/32"]
	if !exists || r2.NextHop != "2.2.2.2" || r2.IfaceIndex != 10 {
		t.Errorf("invalid route for 2.2.2.2/32: %+v", r2)
	}

	r4, exists := routeMap["4.4.4.4/32"]
	if !exists || r4.NextHop != "2.2.2.2" || r4.Metric != 2 {
		t.Errorf("invalid route for 4.4.4.4/32: %+v", r4)
	}

	r5, exists := routeMap["5.5.5.5/32"]
	if !exists || r5.NextHop != "2.2.2.2" || r5.Metric != 3 {
		t.Errorf("invalid route for 5.5.5.5/32: %+v", r5)
	}

	rhna, exists := routeMap["192.168.100.0/24"]
	if !exists || rhna.NextHop != "2.2.2.2" || rhna.Metric != 4 {
		t.Errorf("invalid HNA route: %+v", rhna)
	}

	// Verify lookups
	nextHop, iface, err := spf.GetNextHopForDest("5.5.5.5")
	if err != nil || nextHop != "2.2.2.2" || iface != 10 {
		t.Errorf("failed GetNextHopForDest: got %s, %d, %v", nextHop, iface, err)
	}

	nextHopHna, ifaceHna, err := spf.GetNextHopForDest("192.168.100.50")
	if err != nil || nextHopHna != "2.2.2.2" || ifaceHna != 10 {
		t.Errorf("failed longest prefix match for HNA subnet: got %s, %d, %v", nextHopHna, ifaceHna, err)
	}
}
