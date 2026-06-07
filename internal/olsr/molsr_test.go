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
	"errors"
	"net"
	"testing"
	"time"

	"github.com/sh0jitmy/olsr-go/internal/eventbus"
)

func TestMOLSRManagerDatabases(t *testing.T) {
	bus := eventbus.NewEventBus(10, 10*time.Millisecond)
	m := NewMOLSRManager("1.1.1.1", bus)
	ctx := context.Background()

	// 1. Process Source Claim
	m.ProcessSourceClaim(ctx, net.ParseIP("2.2.2.2"), SourceClaimMessage{
		SourceIP: net.ParseIP("10.10.10.1"),
		GroupID:  net.ParseIP("224.0.0.9"),
	}, 100*time.Millisecond)

	claims := m.GetSourceClaims()
	if len(claims) != 1 || claims[0].SourceIP != "10.10.10.1" || claims[0].OriginatorIP != "2.2.2.2" {
		t.Errorf("failed to process Source Claim, got: %v", claims)
	}

	// 2. Process Confirm Parent
	// Designated parent is 1.1.1.1 (us), child is 3.3.3.3
	m.ProcessConfirmParent(ctx, net.ParseIP("3.3.3.3"), ConfirmParentMessage{
		SourceIP: net.ParseIP("10.10.10.1"),
		GroupID:  net.ParseIP("224.0.0.9"),
		ParentIP: net.ParseIP("1.1.1.1"),
	}, 100*time.Millisecond)

	parents := m.GetConfirmParents()
	if len(parents) != 1 || parents[0].ChildIP != "3.3.3.3" || parents[0].ParentIP != "1.1.1.1" {
		t.Errorf("failed to process Confirm Parent, got: %v", parents)
	}

	// 3. Test Aging
	time.Sleep(150 * time.Millisecond)
	changed := m.AgeOut(ctx)
	if !changed {
		t.Errorf("expected AgeOut to report changes")
	}

	if len(m.GetSourceClaims()) != 0 || len(m.GetConfirmParents()) != 0 {
		t.Errorf("expected all MOLSR DB items to age out")
	}
}

func TestMOLSRMulticastTreeCalculation(t *testing.T) {
	bus := eventbus.NewEventBus(10, 10*time.Millisecond)
	m := NewMOLSRManager("1.1.1.1", bus)
	ctx := context.Background()

	// Mock interface mapping lookup:
	// NextHop "2.2.2.2" -> eth1 (index 10)
	// NextHop "3.3.3.3" -> eth2 (index 20)
	m.IfaceIndexLookup = func(nextHopIP string) (int, error) {
		switch nextHopIP {
		case "2.2.2.2":
			return 10, nil
		case "3.3.3.3":
			return 20, nil
		default:
			return 0, errors.New("not found")
		}
	}

	// Mock Unicast NextHop lookup function
	getUnicastNextHop := func(dest string) (string, int, error) {
		switch dest {
		case "10.10.10.1": // Source
			return "2.2.2.2", 10, nil
		case "3.3.3.3": // Child
			return "3.3.3.3", 20, nil
		default:
			return "", 0, errors.New("no route")
		}
	}

	// Setup confirm parent: Child 3.3.3.3 designates us (1.1.1.1) as parent for flow 10.10.10.1 -> 224.0.0.9
	m.AddLocalConfirmParent(ctx, "10.10.10.1", "224.0.0.9", "1.1.1.1", "3.3.3.3", 10*time.Second)

	// Subscribe to route install events
	sub := bus.Subscribe(eventbus.EventRouteInstall)
	defer bus.Unsubscribe(sub)

	// Recalculate
	m.RecalculateMroutes(ctx, getUnicastNextHop)

	// Verify multicast forwarding entry is built
	mfcs := m.GetActiveMFC()
	if len(mfcs) != 1 {
		t.Fatalf("expected 1 active MFC forwarding entry, got %d", len(mfcs))
	}
	mfc := mfcs[0]
	if mfc.SourceIP != "10.10.10.1" || mfc.GroupID != "224.0.0.9" {
		t.Errorf("mismatch MFC flow details: %+v", mfc)
	}
	if mfc.IIF != 10 {
		t.Errorf("expected IIF 10 (toward source), got %d", mfc.IIF)
	}
	if len(mfc.OIFs) != 1 || mfc.OIFs[0] != 20 {
		t.Errorf("expected OIFs [20] (toward child), got %v", mfc.OIFs)
	}

	// Verify event was fired
	select {
	case ev := <-sub.Out():
		data, ok := ev.Data.(map[string]interface{})
		if !ok || data["action"].(string) != "add_multicast" {
			t.Errorf("expected add_multicast event, got %v", ev)
		}
	default:
		t.Errorf("expected route install event, but none received")
	}
}
