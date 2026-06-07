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

	"github.com/sh0jitmy/olsr-go/internal/eventbus"
)

func TestLinkStateTransitions(t *testing.T) {
	bus := eventbus.NewEventBus(10, 10*time.Millisecond)
	nm := NewNeighborManager("1.1.1.1", bus)
	ctx := context.Background()

	senderIP := net.ParseIP("2.2.2.2")
	originatorIP := net.ParseIP("2.2.2.2")

	// Subscribing to neighbor updates
	sub := bus.Subscribe(eventbus.EventNeighborUpdate)
	defer bus.Unsubscribe(sub)

	// 1. Initial HELLO without our routerID (should transition to Asymmetric)
	helloNoUs := HelloMessage{
		Htime:       2 * time.Second,
		Willingness: WillDefault,
		LinkMessages: []HelloLinkMessage{
			{
				LinkCode:          (LinkTypeAsym & 0x03) | ((NeighTypeNotNeigh & 0x03) << 2),
				NeighborAddresses: []net.IP{net.ParseIP("3.3.3.3")},
			},
		},
	}

	nm.ProcessHello(ctx, senderIP, originatorIP, helloNoUs, 6*time.Second)

	links := nm.GetLinks()
	if len(links) != 1 || links[0].State != LinkStateAsymmetric {
		t.Errorf("expected Asymmetric state, got %v", links)
	}

	// 2. HELLO that lists us as Asymmetric or Symmetric (should transition to Symmetric)
	helloWithUs := HelloMessage{
		Htime:       2 * time.Second,
		Willingness: WillDefault,
		LinkMessages: []HelloLinkMessage{
			{
				LinkCode:          (LinkTypeSym & 0x03) | ((NeighTypeSym & 0x03) << 2),
				NeighborAddresses: []net.IP{net.ParseIP("1.1.1.1")},
			},
		},
	}

	nm.ProcessHello(ctx, senderIP, originatorIP, helloWithUs, 6*time.Second)

	links = nm.GetLinks()
	if len(links) != 1 || links[0].State != LinkStateSymmetric {
		t.Errorf("expected Symmetric state, got %v", links)
	}

	// Verify event was fired
	select {
	case ev := <-sub.Out():
		if ev.Type != eventbus.EventNeighborUpdate {
			t.Errorf("unexpected event: %v", ev)
		}
	default:
		t.Errorf("expected neighbor update event, but none received")
	}
}

func TestMPRSelection(t *testing.T) {
	bus := eventbus.NewEventBus(10, 10*time.Millisecond)
	nm := NewNeighborManager("1.1.1.1", bus)
	ctx := context.Background()

	// Setup topology:
	// Local: 1.1.1.1
	// 1-hop neighbors:
	// - 2.2.2.2 (covers 2-hop: 4.4.4.4, 5.5.5.5)
	// - 3.3.3.3 (covers 2-hop: 5.5.5.5, 6.6.6.6)
	// - 7.7.7.7 (covers 2-hop: 8.8.8.8) (Only link to 8.8.8.8, must be selected)

	// Process HELLO from 2.2.2.2
	hello2 := HelloMessage{
		Htime:       2 * time.Second,
		Willingness: WillDefault,
		LinkMessages: []HelloLinkMessage{
			{
				LinkCode:          (LinkTypeSym & 0x03) | ((NeighTypeSym & 0x03) << 2),
				NeighborAddresses: []net.IP{net.ParseIP("1.1.1.1"), net.ParseIP("4.4.4.4"), net.ParseIP("5.5.5.5")},
			},
		},
	}
	nm.ProcessHello(ctx, net.ParseIP("2.2.2.2"), net.ParseIP("2.2.2.2"), hello2, 6*time.Second)

	// Process HELLO from 3.3.3.3
	hello3 := HelloMessage{
		Htime:       2 * time.Second,
		Willingness: WillDefault,
		LinkMessages: []HelloLinkMessage{
			{
				LinkCode:          (LinkTypeSym & 0x03) | ((NeighTypeSym & 0x03) << 2),
				NeighborAddresses: []net.IP{net.ParseIP("1.1.1.1"), net.ParseIP("5.5.5.5"), net.ParseIP("6.6.6.6")},
			},
		},
	}
	nm.ProcessHello(ctx, net.ParseIP("3.3.3.3"), net.ParseIP("3.3.3.3"), hello3, 6*time.Second)

	// Process HELLO from 7.7.7.7
	hello7 := HelloMessage{
		Htime:       2 * time.Second,
		Willingness: WillDefault,
		LinkMessages: []HelloLinkMessage{
			{
				LinkCode:          (LinkTypeSym & 0x03) | ((NeighTypeSym & 0x03) << 2),
				NeighborAddresses: []net.IP{net.ParseIP("1.1.1.1"), net.ParseIP("8.8.8.8")},
			},
		},
	}
	nm.ProcessHello(ctx, net.ParseIP("7.7.7.7"), net.ParseIP("7.7.7.7"), hello7, 6*time.Second)

	// Verify 2-hop neighbors are learned correctly
	twoHop := nm.GetTwoHopNeighbors()
	if len(twoHop["2.2.2.2"]) != 2 || len(twoHop["3.3.3.3"]) != 2 || len(twoHop["7.7.7.7"]) != 1 {
		t.Errorf("2-hop neighbors setup error: %v", twoHop)
	}

	// Verify MPR Set calculation
	// 7.7.7.7 must be MPR because it's the only one covering 8.8.8.8
	// 2.2.2.2 and 3.3.3.3 cover {4, 5} and {5, 6} respectively.
	// Since 4.4.4.4 and 6.6.6.6 are uniquely covered by 2.2.2.2 and 3.3.3.3 respectively,
	// BOTH 2.2.2.2 and 3.3.3.3 must also be selected to cover all 2-hops.
	mprs := nm.GetMPRSet()
	if len(mprs) != 3 {
		t.Errorf("expected 3 MPR nodes, got %v", mprs)
	}

	mprMap := make(map[string]bool)
	for _, mpr := range mprs {
		mprMap[mpr] = true
	}

	if !mprMap["2.2.2.2"] || !mprMap["3.3.3.3"] || !mprMap["7.7.7.7"] {
		t.Errorf("MPR selection failure, got: %v", mprs)
	}
}

func TestNeighborAging(t *testing.T) {
	bus := eventbus.NewEventBus(10, 10*time.Millisecond)
	nm := NewNeighborManager("1.1.1.1", bus)
	ctx := context.Background()

	// Initial HELLO (expires in 100ms)
	hello := HelloMessage{
		Htime:       2 * time.Second,
		Willingness: WillDefault,
		LinkMessages: []HelloLinkMessage{
			{
				LinkCode:          (LinkTypeSym & 0x03) | ((NeighTypeSym & 0x03) << 2),
				NeighborAddresses: []net.IP{net.ParseIP("1.1.1.1")},
			},
		},
	}
	nm.ProcessHello(ctx, net.ParseIP("2.2.2.2"), net.ParseIP("2.2.2.2"), hello, 100*time.Millisecond)

	links := nm.GetLinks()
	if len(links) != 1 || links[0].State != LinkStateSymmetric {
		t.Fatalf("setup failed, expected Symmetric link")
	}

	// Sleep to exceed 100ms
	time.Sleep(150 * time.Millisecond)

	// Run aging
	changed := nm.AgeOut(ctx)
	if !changed {
		t.Errorf("expected AgeOut to make changes")
	}

	links = nm.GetLinks()
	if len(links) != 1 || links[0].State != LinkStateLost {
		t.Errorf("expected link to age out to Lost state, got %v", links)
	}
}
