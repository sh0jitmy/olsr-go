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

package zebra

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/sh0jitmy/olsr-go/internal/eventbus"
)

func TestZAPIClientUnicastAndMulticast(t *testing.T) {
	socketPath := fmt.Sprintf("/tmp/z-%d.api", time.Now().UnixNano())
	defer func() { _ = os.Remove(socketPath) }()

	// Start a mock Zebra Unix socket server
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("failed to start mock zebra listener: %v", err)
	}
	defer func() { _ = listener.Close() }()

	packetChan := make(chan []byte, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				buf := make([]byte, 1024)
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					// Send parsed data to test channel
					data := make([]byte, n)
					copy(data, buf[:n])
					packetChan <- data
				}
			}(conn)
		}
	}()

	bus := eventbus.NewEventBus(10, 10*time.Millisecond)
	client := NewZAPIClient(socketPath, bus)
	client.Start(ctx)
	defer client.Stop()

	// Wait for connection and HELLO
	var helloPacket []byte
	select {
	case helloPacket = <-packetChan:
		// Check HELLO header
		if len(helloPacket) < 10 {
			t.Fatalf("HELLO packet too short")
		}
		marker := helloPacket[2]
		version := helloPacket[3]
		cmd := binary.BigEndian.Uint16(helloPacket[8:10])
		if marker != ZapiMarker || version != ZapiVersion6 || cmd != 18 {
			t.Errorf("invalid HELLO message headers: marker=%v, version=%v, cmd=%d", marker, version, cmd)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for ZAPI client connection")
	}

	// Test Unicast route insertion
	err = client.AddUnicastRoute("10.0.0.0/24", "192.168.1.1", 5, 2)
	if err != nil {
		t.Fatalf("failed to send unicast route: %v", err)
	}

	select {
	case routePacket := <-packetChan:
		cmd := binary.BigEndian.Uint16(routePacket[8:10])
		if cmd != ZebraRouteAdd {
			t.Errorf("expected cmd ZEBRA_ROUTE_ADD (%d), got %d", ZebraRouteAdd, cmd)
		}
		// check route type OLSR (11) at index 10
		if routePacket[10] != RouteOlsr {
			t.Errorf("expected route type %d, got %d", RouteOlsr, routePacket[10])
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("timeout waiting for unicast packet")
	}

	// Test Multicast route insertion
	err = client.AddMulticastRoute("10.10.10.1", "224.0.0.9", 2, []int{3, 4})
	if err != nil {
		t.Fatalf("failed to send multicast route: %v", err)
	}

	select {
	case mcastPacket := <-packetChan:
		cmd := binary.BigEndian.Uint16(mcastPacket[8:10])
		if cmd != ZebraIpmrRouteAdd {
			t.Errorf("expected cmd ZebraIpmrRouteAdd (%d), got %d", ZebraIpmrRouteAdd, cmd)
		}
		// Check multicast details: Family (2) at index 10
		if mcastPacket[10] != 2 {
			t.Errorf("expected address family 2 (IPv4), got %d", mcastPacket[10])
		}
		// Source IP (10.10.10.1) starts at index 11
		srcIP := net.IP(mcastPacket[11:15])
		if !srcIP.Equal(net.ParseIP("10.10.10.1")) {
			t.Errorf("expected source 10.10.10.1, got %v", srcIP)
		}
		// Group IP (224.0.0.9) starts at index 15
		grpIP := net.IP(mcastPacket[15:19])
		if !grpIP.Equal(net.ParseIP("224.0.0.9")) {
			t.Errorf("expected group 224.0.0.9, got %v", grpIP)
		}
		// IIF (2) starts at index 19
		iif := binary.BigEndian.Uint32(mcastPacket[19:23])
		if iif != 2 {
			t.Errorf("expected IIF 2, got %d", iif)
		}
		// OIF Count (2) starts at index 23
		oifCount := binary.BigEndian.Uint32(mcastPacket[23:27])
		if oifCount != 2 {
			t.Errorf("expected 2 OIFs, got %d", oifCount)
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("timeout waiting for multicast packet")
	}
}

func TestZAPIClientOfflineError(t *testing.T) {
	bus := eventbus.NewEventBus(10, 10*time.Millisecond)
	// Try to connect to non-existent path
	client := NewZAPIClient("/tmp/nonexistent-zebra-socket-path", bus)

	err := client.AddUnicastRoute("10.0.0.0/24", "192.168.1.1", 5, 1)
	if err == nil {
		t.Errorf("expected error on offline client")
	}
}
