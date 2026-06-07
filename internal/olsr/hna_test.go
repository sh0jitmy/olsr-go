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

func TestHNAManagerLocal(t *testing.T) {
	bus := eventbus.NewEventBus(10, 10*time.Millisecond)
	hm := NewHNAManager(bus)
	ctx := context.Background()

	_, ipNet, _ := net.ParseCIDR("192.168.10.0/24")
	hm.AddLocalHNA(ctx, *ipNet)

	locals := hm.GetLocalHNAs()
	if len(locals) != 1 || locals[0].String() != "192.168.10.0/24" {
		t.Errorf("failed to add local HNA, got: %v", locals)
	}

	entries := hm.GetHNAEntries()
	if len(entries) != 1 || !entries[0].IsLocal || entries[0].GatewayIP != "local" {
		t.Errorf("expected local entry in all entries list, got: %v", entries)
	}

	hm.RemoveLocalHNA(ctx, *ipNet)
	locals = hm.GetLocalHNAs()
	if len(locals) != 0 {
		t.Errorf("failed to remove local HNA, got: %v", locals)
	}
}

func TestHNAManagerRemoteAndAging(t *testing.T) {
	bus := eventbus.NewEventBus(10, 10*time.Millisecond)
	hm := NewHNAManager(bus)
	ctx := context.Background()

	orig := net.ParseIP("2.2.2.2")
	_, ipNet, _ := net.ParseCIDR("10.10.0.0/16")

	hna := HNAMessage{
		Associations: []HNAAssociation{
			{
				Address: ipNet.IP,
				Netmask: *ipNet,
			},
		},
	}

	hm.ProcessHNA(ctx, orig, hna, 100*time.Millisecond)

	entries := hm.GetHNAEntries()
	if len(entries) != 1 || entries[0].IsLocal || entries[0].GatewayIP != "2.2.2.2" {
		t.Fatalf("failed to process remote HNA, got: %v", entries)
	}

	time.Sleep(150 * time.Millisecond)
	changed := hm.AgeOut(ctx)
	if !changed {
		t.Errorf("expected AgeOut to report changes")
	}

	entries = hm.GetHNAEntries()
	if len(entries) != 0 {
		t.Errorf("expected remote HNA to age out, got: %v", entries)
	}
}
