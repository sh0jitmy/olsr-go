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

	"github.com/shjtmy/olsr-go/internal/eventbus"
)

func TestDuplicateSuppression(t *testing.T) {
	bus := eventbus.NewEventBus(10, 10*time.Millisecond)
	tm := NewTopologyManager(bus)
	orig := net.ParseIP("2.2.2.2")

	// First reception: not duplicate
	if tm.IsDuplicate(orig, 100, 100*time.Millisecond) {
		t.Errorf("expected first packet not to be duplicate")
	}

	// Second reception: duplicate
	if !tm.IsDuplicate(orig, 100, 100*time.Millisecond) {
		t.Errorf("expected duplicate packet to be blocked")
	}

	// Different sequence number: not duplicate
	if tm.IsDuplicate(orig, 101, 100*time.Millisecond) {
		t.Errorf("expected different seq num not to be duplicate")
	}
}

func TestTCProcessingAndAging(t *testing.T) {
	bus := eventbus.NewEventBus(10, 10*time.Millisecond)
	tm := NewTopologyManager(bus)
	ctx := context.Background()

	orig := net.ParseIP("2.2.2.2")
	tc := TCMessage{
		ANSN:              1,
		NeighborAddresses: []net.IP{net.ParseIP("3.3.3.3"), net.ParseIP("4.4.4.4")},
	}

	// 1. Process TC
	tm.ProcessTC(ctx, orig, tc, 100*time.Millisecond)

	topo := tm.GetTopology()
	if len(topo) != 2 {
		t.Fatalf("expected 2 topology tuples, got %d", len(topo))
	}

	destMap := make(map[string]bool)
	for _, tuple := range topo {
		if tuple.LastIP != "2.2.2.2" {
			t.Errorf("expected LastIP to be 2.2.2.2, got %s", tuple.LastIP)
		}
		destMap[tuple.DestIP] = true
	}
	if !destMap["3.3.3.3"] || !destMap["4.4.4.4"] {
		t.Errorf("expected dest addresses to match TC contents, got: %v", destMap)
	}

	// 2. Wait for aging
	time.Sleep(150 * time.Millisecond)
	changed := tm.AgeOut(ctx)
	if !changed {
		t.Errorf("expected AgeOut to report changes")
	}

	topo = tm.GetTopology()
	if len(topo) != 0 {
		t.Errorf("expected all topology tuples to age out, got %d", len(topo))
	}
}

func TestMIDProcessing(t *testing.T) {
	bus := eventbus.NewEventBus(10, 10*time.Millisecond)
	tm := NewTopologyManager(bus)
	ctx := context.Background()

	primary := net.ParseIP("2.2.2.2")
	mid := MIDMessage{
		Addresses: []net.IP{net.ParseIP("10.0.0.2"), net.ParseIP("192.168.0.2")},
	}

	tm.ProcessMID(ctx, primary, mid, 10*time.Second)

	// Verify alias lookup
	if tm.GetPrimaryAddress("10.0.0.2") != "2.2.2.2" {
		t.Errorf("expected alias 10.0.0.2 to map to primary 2.2.2.2")
	}
	if tm.GetPrimaryAddress("192.168.0.2") != "2.2.2.2" {
		t.Errorf("expected alias 192.168.0.2 to map to primary 2.2.2.2")
	}
	if tm.GetPrimaryAddress("2.2.2.2") != "2.2.2.2" {
		t.Errorf("expected non-alias IP to map to itself")
	}
}
