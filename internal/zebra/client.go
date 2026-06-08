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
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"math/rand"
	"net"
	"sync"
	"time"

	"github.com/sh0jitmy/olsr-go/internal/eventbus"
)

// ZAPI Unicast Command Constants
const (
	ZebraRouteAdd    uint16 = 8
	ZebraRouteDelete uint16 = 9
	RouteOlsr        uint8  = 14 // ZEBRA_ROUTE_OLSR
)

type ZAPIClient struct {
	mu            sync.Mutex
	socketPath    string
	conn          net.Conn
	connected     bool
	eventBus      *eventbus.EventBus
	cancel        context.CancelFunc
	reconnectChan chan struct{}

	// Metrics
	txTotal      uint64
	failureTotal uint64

	// Sync callbacks to retrieve active routes upon reconnection
	GetActiveUnicastRoutes   func() []UnicastRouteInfo
	GetActiveMulticastRoutes func() []MulticastRouteInfo
}

type UnicastRouteInfo struct {
	Prefix     string
	NextHop    string
	IfaceIndex int
	Metric     int
}

type MulticastRouteInfo struct {
	SourceIP string
	GroupID  string
	IIF      int
	OIFs     []int
}

func NewZAPIClient(socketPath string, bus *eventbus.EventBus) *ZAPIClient {
	return &ZAPIClient{
		socketPath:    socketPath,
		eventBus:      bus,
		reconnectChan: make(chan struct{}, 1),
	}
}

// Start initiates the Zebra connection loop.
func (c *ZAPIClient) Start(ctx context.Context) {
	c.mu.Lock()
	var runCtx context.Context
	runCtx, c.cancel = context.WithCancel(ctx)
	c.mu.Unlock()
	go c.connectionLoop(runCtx)
	c.reconnectChan <- struct{}{} // Trigger initial connection
}

// Stop shuts down the client and closes connection.
func (c *ZAPIClient) Stop() {
	c.mu.Lock()
	if c.cancel != nil {
		c.cancel()
	}
	if c.conn != nil {
		_ = c.conn.Close()
	}
	c.connected = false
	c.mu.Unlock()
}

func (c *ZAPIClient) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}

func (c *ZAPIClient) GetMetrics() (connected bool, tx uint64, failures uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected, c.txTotal, c.failureTotal
}

func (c *ZAPIClient) connectionLoop(ctx context.Context) {
	backoff := 1 * time.Second
	maxBackoff := 30 * time.Second

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.reconnectChan:
			err := c.connect()
			if err != nil {
				c.mu.Lock()
				c.failureTotal++
				c.mu.Unlock()
				slog.Error("ZAPI connect failed", "socket", c.socketPath, "error", err)

				// Exponential backoff with jitter
				//nolint:gosec // G404: weak random number generator is safe/appropriate for network backoff jitter
				jitter := time.Duration(rand.Int63n(int64(backoff / 2)))
				sleepTime := backoff + jitter
				if backoff < maxBackoff {
					backoff *= 2
				}

				// Publish metrics update (ZAPI disconnected)
				c.eventBus.Publish(ctx, eventbus.Event{
					Type: eventbus.EventRouteInstall,
					Data: map[string]interface{}{"status": "disconnected"},
				})

				select {
				case <-ctx.Done():
					return
				case <-time.After(sleepTime):
					// Retry connection
					select {
					case c.reconnectChan <- struct{}{}:
					default:
					}
				}
			} else {
				// Connected successfully
				backoff = 1 * time.Second
				c.mu.Lock()
				c.connected = true
				c.mu.Unlock()
				slog.Info("ZAPI connected successfully", "socket", c.socketPath)

				// Publish metrics update (ZAPI connected)
				c.eventBus.Publish(ctx, eventbus.Event{
					Type: eventbus.EventRouteInstall,
					Data: map[string]interface{}{"status": "connected"},
				})

				// Trigger synchronization of current active routes
				c.syncRoutes()

				// Start reading from socket to detect remote close
				go c.readLoop()
			}
		}
	}
}

func (c *ZAPIClient) connect() error {
	c.mu.Lock()
	if c.conn != nil {
		_ = c.conn.Close()
	}
	c.connected = false
	c.mu.Unlock()

	conn, err := net.Dial("unix", c.socketPath)
	if err != nil {
		return err
	}

	// Send HELLO / Registration command to Zebra (zserv_hello)
	// For ZAPI v6, registration as client is ZEBRA_HELLO (command 1)
	helloMsg := buildHelloMessage()
	_, err = conn.Write(helloMsg)
	if err != nil {
		_ = conn.Close()
		return err
	}

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	return nil
}

func (c *ZAPIClient) readLoop() {
	buf := make([]byte, 1024)
	for {
		c.mu.Lock()
		conn := c.conn
		c.mu.Unlock()

		if conn == nil {
			return
		}

		_, err := conn.Read(buf)
		if err != nil {
			// Connection lost
			c.mu.Lock()
			c.connected = false
			c.mu.Unlock()

			// Trigger reconnect
			select {
			case c.reconnectChan <- struct{}{}:
			default:
			}
			return
		}
	}
}

func (c *ZAPIClient) syncRoutes() {
	if c.GetActiveUnicastRoutes != nil {
		routes := c.GetActiveUnicastRoutes()
		for _, r := range routes {
			_ = c.AddUnicastRoute(r.Prefix, r.NextHop, r.IfaceIndex, r.Metric)
		}
	}
	// Multicast routes are programmed directly in the kernel, not via ZAPI.
	/*
		if c.GetActiveMulticastRoutes != nil {
			routes := c.GetActiveMulticastRoutes()
			for _, r := range routes {
				_ = c.AddMulticastRoute(r.SourceIP, r.GroupID, r.IIF, r.OIFs)
			}
		}
	*/
}

// AddUnicastRoute registers a unicast prefix to Zebra
func (c *ZAPIClient) AddUnicastRoute(prefix string, nexthop string, ifindex int, metric int) error {
	data, err := buildUnicastRouteMessage(ZebraRouteAdd, prefix, nexthop, ifindex, metric)
	if err != nil {
		return err
	}
	return c.send(data)
}

// DeleteUnicastRoute removes a unicast prefix from Zebra
func (c *ZAPIClient) DeleteUnicastRoute(prefix string, nexthop string, ifindex int) error {
	data, err := buildUnicastRouteMessage(ZebraRouteDelete, prefix, nexthop, ifindex, 0)
	if err != nil {
		return err
	}
	return c.send(data)
}

// AddMulticastRoute registers a multicast route to Zebra
func (c *ZAPIClient) AddMulticastRoute(srcIP, grpIP string, iif int, oifs []int) error {
	data, err := BuildIpmrMessage(ZebraIpmrRouteAdd, net.ParseIP(srcIP), net.ParseIP(grpIP), iif, oifs)
	if err != nil {
		return err
	}
	return c.send(data)
}

// DeleteMulticastRoute removes a multicast route from Zebra
func (c *ZAPIClient) DeleteMulticastRoute(srcIP, grpIP string, iif int, oifs []int) error {
	data, err := BuildIpmrMessage(ZebraIpmrRouteDel, net.ParseIP(srcIP), net.ParseIP(grpIP), iif, oifs)
	if err != nil {
		return err
	}
	return c.send(data)
}

func (c *ZAPIClient) send(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected || c.conn == nil {
		c.failureTotal++
		return fmt.Errorf("zapi client not connected")
	}

	_, err := c.conn.Write(data)
	if err != nil {
		c.failureTotal++
		return err
	}

	c.txTotal++
	return nil
}

func buildHelloMessage() []byte {
	buf := new(bytes.Buffer)
	// Header (Size placeholder, Marker, Version, VRF, Command)
	_ = binary.Write(buf, binary.BigEndian, uint16(0))
	buf.WriteByte(ZapiMarker)
	buf.WriteByte(ZapiVersion6)
	_ = binary.Write(buf, binary.BigEndian, uint32(0))  // VRF ID (uint32)
	_ = binary.Write(buf, binary.BigEndian, uint16(18)) // Command (uint16)

	// Body
	buf.WriteByte(RouteOlsr)                           // Route Type (OLSR)
	_ = binary.Write(buf, binary.BigEndian, uint16(0)) // Instance
	_ = binary.Write(buf, binary.BigEndian, uint32(0)) // Session ID (uint32)
	buf.WriteByte(0)                                   // Receive Notify (uint8)
	buf.WriteByte(0)                                   // Synchronous (uint8)

	data := buf.Bytes()
	if len(data) > 65535 {
		panic("hello message size exceeds maximum uint16 size")
	}
	//nolint:gosec // G115: length is pre-validated to be <= 65535
	binary.BigEndian.PutUint16(data[0:2], uint16(len(data)))
	return data
}

func buildUnicastRouteMessage(cmd uint16, prefix string, nexthop string, ifindex int, metric int) ([]byte, error) {
	ip, ipNet, err := net.ParseCIDR(prefix)
	if err != nil {
		return nil, err
	}
	ones, _ := ipNet.Mask.Size()
	ip4 := ip.To4()
	nh4 := net.ParseIP(nexthop).To4()
	if ip4 == nil || nh4 == nil {
		return nil, fmt.Errorf("only IPv4 is supported in this OLSR implementation")
	}

	buf := new(bytes.Buffer)
	// --- Header ---
	_ = binary.Write(buf, binary.BigEndian, uint16(0))
	buf.WriteByte(ZapiMarker)
	buf.WriteByte(ZapiVersion6)
	_ = binary.Write(buf, binary.BigEndian, uint32(0)) // VRF ID (uint32)
	_ = binary.Write(buf, binary.BigEndian, cmd)       // Command (uint16)

	// --- Body ---
	buf.WriteByte(RouteOlsr)                              // Route Type (11)
	_ = binary.Write(buf, binary.BigEndian, uint16(0))    // Instance
	_ = binary.Write(buf, binary.BigEndian, uint32(0))    // Flags
	_ = binary.Write(buf, binary.BigEndian, uint32(0x07)) // Message Flags: NEXTHOP (0x01) | DISTANCE (0x02) | METRIC (0x04)
	buf.WriteByte(1)                                      // SAFI (UNICAST = 1)
	buf.WriteByte(2)                                      // Family (AF_INET = 2)

	psize := (ones + 7) / 8
	if ones < 0 || ones > 32 {
		return nil, fmt.Errorf("invalid IPv4 prefix length: %d", ones)
	}
	buf.WriteByte(uint8(ones)) // Prefix Length
	buf.Write(ip4[:psize])     // Prefix IP

	// Nexthops
	_ = binary.Write(buf, binary.BigEndian, uint16(1)) // Nexthop Count (uint16)
	_ = binary.Write(buf, binary.BigEndian, uint32(0)) // Nexthop VRF ID (uint32)
	buf.WriteByte(2)                                   // Nexthop Type (NEXTHOP_TYPE_IPV4 = 2)
	buf.WriteByte(0)                                   // Nexthop Flags (uint8)
	buf.Write(nh4)                                     // Nexthop Address
	if ifindex < 0 {
		return nil, fmt.Errorf("invalid interface index: %d", ifindex)
	}
	//nolint:gosec // G115: value is pre-validated to be >= 0
	_ = binary.Write(buf, binary.BigEndian, uint32(ifindex)) // Nexthop Ifindex (uint32)

	// Distance
	buf.WriteByte(150) // Distance (uint8)

	// Metrics
	if metric < 0 {
		return nil, fmt.Errorf("invalid metric: %d", metric)
	}
	//nolint:gosec // G115: value is pre-validated to be >= 0
	_ = binary.Write(buf, binary.BigEndian, uint32(metric)) // Metric

	data := buf.Bytes()
	if len(data) > 65535 {
		return nil, fmt.Errorf("unicast route message size exceeds maximum uint16 size")
	}
	//nolint:gosec // G115: length is pre-validated to be <= 65535
	binary.BigEndian.PutUint16(data[0:2], uint16(len(data)))
	return data, nil
}
