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

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/shjtmy/olsr-go/internal/api"
	"github.com/shjtmy/olsr-go/internal/config"
	"github.com/shjtmy/olsr-go/internal/eventbus"
	"github.com/shjtmy/olsr-go/internal/metrics"
	"github.com/shjtmy/olsr-go/internal/mroute"
	"github.com/shjtmy/olsr-go/internal/netlink"
	"github.com/shjtmy/olsr-go/internal/olsr"
	"github.com/shjtmy/olsr-go/internal/uroute"
	"github.com/shjtmy/olsr-go/internal/zebra"
)

type Daemon struct {
	cfgMgr       *config.Manager
	eventBus     *eventbus.EventBus
	neighMgr     *olsr.NeighborManager
	topoMgr      *olsr.TopologyManager
	hnaMgr       *olsr.HNAManager
	molsrMgr     *olsr.MOLSRManager
	spfEngine    *olsr.SPFEngine
	zapiClient   *zebra.ZAPIClient
	urouteRouter uroute.UnicastRouter
	mrouteRouter mroute.MulticastRouter
	monitor      netlink.Monitor
	apiServer    *api.APIServer
	Standalone   bool
	
	packetSeq  uint16
	messageSeq uint16
	seqMu      sync.Mutex

	ctx    context.Context
	cancel context.CancelFunc
}

func (d *Daemon) initializeComponents() {
	cfg := d.cfgMgr.Get()

	d.neighMgr = olsr.NewNeighborManager(cfg.RouterID, d.eventBus)
	d.topoMgr = olsr.NewTopologyManager(d.eventBus)
	d.hnaMgr = olsr.NewHNAManager(d.eventBus)
	d.molsrMgr = olsr.NewMOLSRManager(cfg.RouterID, d.eventBus)
	
	// Interface mappings helper for SPF
	lookup := olsr.LocalRouterLookup{
		RouterID: cfg.RouterID,
		IfaceIndexLookup: func(nextHop string) int {
			return d.monitor.GetIfaceIndex(d.findOutgoingInterface(nextHop))
		},
	}
	d.spfEngine = olsr.NewSPFEngine(lookup, d.neighMgr, d.topoMgr, d.hnaMgr, d.eventBus)
	
	if cfg.Standalone {
		d.Standalone = true
	}
	if !d.Standalone {
		d.zapiClient = zebra.NewZAPIClient(cfg.ZAPIAddress, d.eventBus)
	}
	d.urouteRouter = uroute.NewUnicastRouter()
	d.mrouteRouter = mroute.NewMulticastRouter()
	d.monitor = netlink.NewMonitor(d.eventBus)
	
	// API Server
	d.apiServer = api.NewAPIServer(d.cfgMgr, d.neighMgr, d.topoMgr, d.hnaMgr, d.molsrMgr, d.spfEngine, d.zapiClient, d.monitor)

	// Inject route fetch functions into ZAPI Client for synchronizations
	if d.zapiClient != nil {
		d.zapiClient.GetActiveUnicastRoutes = func() []zebra.UnicastRouteInfo {
			spfRoutes := d.spfEngine.GetRoutes()
			routes := make([]zebra.UnicastRouteInfo, len(spfRoutes))
			for i, r := range spfRoutes {
				routes[i] = zebra.UnicastRouteInfo{
					Prefix:     r.Prefix,
					NextHop:    r.NextHop,
					IfaceIndex: r.IfaceIndex,
					Metric:     r.Metric,
				}
			}
			return routes
		}
		d.zapiClient.GetActiveMulticastRoutes = func() []zebra.MulticastRouteInfo {
			mfcEntries := d.molsrMgr.GetActiveMFC()
			routes := make([]zebra.MulticastRouteInfo, len(mfcEntries))
			for i, entry := range mfcEntries {
				routes[i] = zebra.MulticastRouteInfo{
					SourceIP: entry.SourceIP,
					GroupID:  entry.GroupID,
					IIF:      entry.IIF,
					OIFs:     entry.OIFs,
				}
			}
			return routes
		}
	}

	// Link MOLSR interface index lookups
	d.molsrMgr.IfaceIndexLookup = func(nextHopIP string) (int, error) {
		idx := d.monitor.GetIfaceIndex(d.findOutgoingInterface(nextHopIP))
		if idx == 0 {
			return 0, fmt.Errorf("interface index not found")
		}
		return idx, nil
	}
}

func (d *Daemon) findOutgoingInterface(nextHop string) string {
	nhIP := net.ParseIP(nextHop)
	if nhIP == nil {
		return ""
	}
	ifaces := d.monitor.GetInterfaces()
	for _, iface := range ifaces {
		for _, ipStr := range iface.IPs {
			ip := net.ParseIP(ipStr)
			if ip != nil && ip.To4() != nil {
				if ip.To4()[0] == nhIP.To4()[0] && ip.To4()[1] == nhIP.To4()[1] && ip.To4()[2] == nhIP.To4()[2] {
					return iface.Name
				}
			}
		}
	}
	cfg := d.cfgMgr.Get()
	if len(cfg.Interfaces) > 0 {
		return cfg.Interfaces[0]
	}
	return ""
}

func (d *Daemon) handleSignals(sigChan chan os.Signal) {
	for sig := range sigChan {
		switch sig {
		case syscall.SIGHUP:
			slog.Info("SIGHUP received, reloading configuration...")
			if err := d.cfgMgr.Reload(); err != nil {
				slog.Error("Configuration reload rejected, rolled back to original", "error", err)
			} else {
				slog.Info("Configuration successfully reloaded")
			}
		case syscall.SIGINT, syscall.SIGTERM:
			slog.Info("Termination signal received. Shutting down gracefully...")
			d.Stop()
			return
		}
	}
}

func (d *Daemon) Stop() {
	d.cancel()
	d.apiServer.Stop(context.Background())
	if d.zapiClient != nil {
		d.zapiClient.Stop()
	}
	if d.mrouteRouter != nil {
		_ = d.mrouteRouter.Stop()
	}
	d.monitor.Stop()
	slog.Info("OLSR Go Routing Daemon stopped.")
}

func (d *Daemon) run() {
	metricsStop := make(chan struct{})
	defer close(metricsStop)
	metrics.StartSystemMetricsCollector(metricsStop)

	d.monitor.Start(d.ctx)
	if d.zapiClient != nil {
		d.zapiClient.Start(d.ctx)
	}
	if d.mrouteRouter != nil {
		if err := d.mrouteRouter.Start(); err != nil {
			slog.Error("Failed to start kernel multicast router", "error", err)
		}
	}
	_ = d.apiServer.Start(d.ctx)

	subRoutes := d.eventBus.Subscribe(eventbus.EventRouteInstall)
	subNeighbors := d.eventBus.Subscribe(eventbus.EventNeighborUpdate)
	subTopology := d.eventBus.Subscribe(eventbus.EventTopologyUpdate)
	subSPF := d.eventBus.Subscribe(eventbus.EventSPFTrigger)

	defer func() {
		d.eventBus.Unsubscribe(subRoutes)
		d.eventBus.Unsubscribe(subNeighbors)
		d.eventBus.Unsubscribe(subTopology)
		d.eventBus.Unsubscribe(subSPF)
	}()

	go d.routeInstallationLoop(subRoutes)
	go d.topologyUpdateLoop(subNeighbors, subTopology)
	go d.spfExecutionMetricsLoop(subSPF)
	go d.udpReceiverLoop()

	cfg := d.cfgMgr.Get()
	helloTicker := time.NewTicker(cfg.HelloInterval)
	tcTicker := time.NewTicker(cfg.TCInterval)
	hnaTicker := time.NewTicker(cfg.TCInterval)
	agingTicker := time.NewTicker(500 * time.Millisecond)
	snapshotTicker := time.NewTicker(10 * time.Second)

	defer helloTicker.Stop()
	defer tcTicker.Stop()
	defer hnaTicker.Stop()
	defer agingTicker.Stop()
	defer snapshotTicker.Stop()

	d.sendHello()

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-helloTicker.C:
			d.sendHello()
		case <-tcTicker.C:
			d.sendTC()
		case <-hnaTicker.C:
			d.sendHNA()
		case <-agingTicker.C:
			d.performAging()
		case <-snapshotTicker.C:
			d.saveSnapshots()
		}
	}
}

func (d *Daemon) routeInstallationLoop(sub *eventbus.Subscription) {
	for ev := range sub.Out() {
		data, ok := ev.Data.(map[string]interface{})
		if !ok {
			continue
		}

		if statusVal, ok := data["status"]; ok {
			if status, ok := statusVal.(string); ok {
				switch status {
				case "connected":
					metrics.ZapiConnected.Set(1)
				case "disconnected":
					metrics.ZapiConnected.Set(0)
				}
			}
			continue
		}

		actionVal, ok := data["action"]
		if !ok {
			continue
		}
		action, ok := actionVal.(string)
		if !ok {
			continue
		}

		switch action {
		case "add":
			route := data["route"].(olsr.Route)
			if d.Standalone {
				err := d.urouteRouter.AddRoute(route.Prefix, route.NextHop, route.IfaceIndex, route.Metric)
				if err != nil {
					slog.Error("Standalone Unicast add failed", "prefix", route.Prefix, "error", err)
				} else {
					slog.Info("Standalone Unicast route added", "prefix", route.Prefix, "nextHop", route.NextHop)
				}
			} else if d.zapiClient != nil {
				err := d.zapiClient.AddUnicastRoute(route.Prefix, route.NextHop, route.IfaceIndex, route.Metric)
				if err != nil {
					slog.Error("ZAPI Unicast add failed", "prefix", route.Prefix, "error", err)
					metrics.ZapiFailureTotal.Inc()
				} else {
					metrics.ZapiTxTotal.Inc()
				}
			}
		case "delete":
			route := data["route"].(olsr.Route)
			if d.Standalone {
				err := d.urouteRouter.DeleteRoute(route.Prefix, route.NextHop, route.IfaceIndex)
				if err != nil {
					slog.Error("Standalone Unicast delete failed", "prefix", route.Prefix, "error", err)
				} else {
					slog.Info("Standalone Unicast route deleted", "prefix", route.Prefix)
				}
			} else if d.zapiClient != nil {
				err := d.zapiClient.DeleteUnicastRoute(route.Prefix, route.NextHop, route.IfaceIndex)
				if err != nil {
					slog.Error("ZAPI Unicast delete failed", "prefix", route.Prefix, "error", err)
					metrics.ZapiFailureTotal.Inc()
				} else {
					metrics.ZapiTxTotal.Inc()
				}
			}
		case "add_multicast":
			entry := data["entry"].(*olsr.MulticastForwardingEntry)
			if d.mrouteRouter != nil {
				err := d.mrouteRouter.AddMulticastRoute(entry.SourceIP, entry.GroupID, entry.IIF, entry.OIFs)
				if err != nil {
					slog.Error("Kernel Multicast add failed", "src", entry.SourceIP, "grp", entry.GroupID, "error", err)
					metrics.ZapiFailureTotal.Inc()
				} else {
					metrics.ZapiTxTotal.Inc()
				}
			}
		case "delete_multicast":
			entry := data["entry"].(*olsr.MulticastForwardingEntry)
			if d.mrouteRouter != nil {
				err := d.mrouteRouter.DeleteMulticastRoute(entry.SourceIP, entry.GroupID, entry.IIF, entry.OIFs)
				if err != nil {
					slog.Error("Kernel Multicast delete failed", "src", entry.SourceIP, "grp", entry.GroupID, "error", err)
					metrics.ZapiFailureTotal.Inc()
				} else {
					metrics.ZapiTxTotal.Inc()
				}
			}
		}
	}
}

func (d *Daemon) topologyUpdateLoop(subNeigh *eventbus.Subscription, subTopo *eventbus.Subscription) {
	runSpf := func() {
		err := d.spfEngine.CalculateRoutes(d.ctx)
		if err != nil {
			slog.Error("SPF route calculation failed", "error", err)
		}
		d.molsrMgr.RecalculateMroutes(d.ctx, func(dest string) (string, int, error) {
			return d.spfEngine.GetNextHopForDest(dest)
		})
	}

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-subNeigh.Out():
			runSpf()
		case <-subTopo.Out():
			runSpf()
		}
	}
}

func (d *Daemon) spfExecutionMetricsLoop(sub *eventbus.Subscription) {
	for ev := range sub.Out() {
		data, ok := ev.Data.(map[string]interface{})
		if ok {
			metrics.SPFRunsTotal.Inc()
			metrics.SPFDurationSeconds.Set(data["duration"].(float64))
			metrics.RouteCount.Set(float64(data["route_count"].(int)))
		}
	}
}

func (d *Daemon) performAging() {
	changedNeigh := d.neighMgr.AgeOut(d.ctx)
	changedTopo := d.topoMgr.AgeOut(d.ctx)
	changedHNA := d.hnaMgr.AgeOut(d.ctx)
	changedMolsr := d.molsrMgr.AgeOut(d.ctx)

	neighs := d.neighMgr.GetNeighbors()
	metrics.NeighborTotal.Set(float64(len(neighs)))

	symCount := 0
	for _, n := range neighs {
		if n.Symmetric {
			symCount++
		}
	}
	metrics.NeighborSymmetricTotal.Set(float64(symCount))

	// AgeOut return values indicate if any changes occurred.
	// Recalculations happen automatically via the eventbus.
	_ = changedNeigh
	_ = changedTopo
	_ = changedHNA
	_ = changedMolsr
}

func (d *Daemon) sendHello() {
	cfg := d.cfgMgr.Get()
	d.seqMu.Lock()
	pktSeq := d.packetSeq
	d.packetSeq++
	msgSeq := d.messageSeq
	d.messageSeq++
	d.seqMu.Unlock()

	links := d.neighMgr.GetLinks()
	linkMsgs := make([]olsr.HelloLinkMessage, 0)
	
	symAddrs := make([]net.IP, 0)
	asymAddrs := make([]net.IP, 0)
	lostAddrs := make([]net.IP, 0)

	for _, l := range links {
		ip := net.ParseIP(l.NeighborIP)
		if ip == nil {
			continue
		}
		switch l.State {
		case olsr.LinkStateSymmetric:
			symAddrs = append(symAddrs, ip)
		case olsr.LinkStateAsymmetric:
			asymAddrs = append(asymAddrs, ip)
		case olsr.LinkStateLost:
			lostAddrs = append(lostAddrs, ip)
		}
	}

	if len(symAddrs) > 0 {
		linkMsgs = append(linkMsgs, olsr.HelloLinkMessage{
			LinkCode:          (olsr.LinkTypeSym & 0x03) | ((olsr.NeighTypeSym & 0x03) << 2),
			NeighborAddresses: symAddrs,
		})
	}
	if len(asymAddrs) > 0 {
		linkMsgs = append(linkMsgs, olsr.HelloLinkMessage{
			LinkCode:          (olsr.LinkTypeAsym & 0x03) | ((olsr.NeighTypeNotNeigh & 0x03) << 2),
			NeighborAddresses: asymAddrs,
		})
	}
	if len(lostAddrs) > 0 {
		linkMsgs = append(linkMsgs, olsr.HelloLinkMessage{
			LinkCode:          (olsr.LinkTypeLost & 0x03) | ((olsr.NeighTypeNotNeigh & 0x03) << 2),
			NeighborAddresses: lostAddrs,
		})
	}

	pkt := &olsr.Packet{
		Header: olsr.PacketHeader{
			PacketSeqNum: pktSeq,
		},
		Messages: []olsr.Message{
			{
				Header: olsr.MessageHeader{
					Type:              olsr.MsgTypeHello,
					Vtime:             6 * time.Second,
					OriginatorAddress: net.ParseIP(cfg.RouterID),
					TTL:               1,
					HopCount:          0,
					SeqNum:            msgSeq,
				},
				Body: olsr.HelloMessage{
					Htime:        cfg.HelloInterval,
					Willingness:  olsr.WillDefault,
					LinkMessages: linkMsgs,
				},
			},
		},
	}

	d.broadcastPacket(pkt)
	metrics.HelloTxTotal.Inc()
}

func (d *Daemon) sendTC() {
	cfg := d.cfgMgr.Get()
	selectors := d.neighMgr.GetMPRSelectors()
	if len(selectors) == 0 {
		return
	}

	d.seqMu.Lock()
	pktSeq := d.packetSeq
	d.packetSeq++
	msgSeq := d.messageSeq
	d.messageSeq++
	d.seqMu.Unlock()

	neighAddrs := make([]net.IP, len(selectors))
	for i, sel := range selectors {
		neighAddrs[i] = net.ParseIP(sel)
	}

	pkt := &olsr.Packet{
		Header: olsr.PacketHeader{
			PacketSeqNum: pktSeq,
		},
		Messages: []olsr.Message{
			{
				Header: olsr.MessageHeader{
					Type:              olsr.MsgTypeTC,
					Vtime:             15 * time.Second,
					OriginatorAddress: net.ParseIP(cfg.RouterID),
					TTL:               255,
					HopCount:          0,
					SeqNum:            msgSeq,
				},
				Body: olsr.TCMessage{
					ANSN:              msgSeq,
					NeighborAddresses: neighAddrs,
				},
			},
		},
	}

	d.broadcastPacket(pkt)
	metrics.TCTxTotal.Inc()
}

func (d *Daemon) sendHNA() {
	cfg := d.cfgMgr.Get()
	localHNAs := d.hnaMgr.GetLocalHNAs()
	if len(localHNAs) == 0 {
		return
	}

	d.seqMu.Lock()
	pktSeq := d.packetSeq
	d.packetSeq++
	msgSeq := d.messageSeq
	d.messageSeq++
	d.seqMu.Unlock()

	associations := make([]olsr.HNAAssociation, len(localHNAs))
	for i, hna := range localHNAs {
		associations[i] = olsr.HNAAssociation{
			Address: hna.IP,
			Netmask: hna,
		}
	}

	pkt := &olsr.Packet{
		Header: olsr.PacketHeader{
			PacketSeqNum: pktSeq,
		},
		Messages: []olsr.Message{
			{
				Header: olsr.MessageHeader{
					Type:              olsr.MsgTypeHNA,
					Vtime:             15 * time.Second,
					OriginatorAddress: net.ParseIP(cfg.RouterID),
					TTL:               255,
					HopCount:          0,
					SeqNum:            msgSeq,
				},
				Body: olsr.HNAMessage{
					Associations: associations,
				},
			},
		},
	}

	d.broadcastPacket(pkt)
}

func (d *Daemon) broadcastPacket(pkt *olsr.Packet) {
	data, err := olsr.SerializePacket(pkt)
	if err != nil {
		slog.Error("Failed to serialize outgoing packet", "error", err)
		return
	}

	cfg := d.cfgMgr.Get()
	for _, ifaceName := range cfg.Interfaces {
		iface, err := net.InterfaceByName(ifaceName)
		if err != nil {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil || len(addrs) == 0 {
			continue
		}

		var localIP net.IP
		for _, addr := range addrs {
			if ipNet, ok := addr.(*net.IPNet); ok && ipNet.IP.To4() != nil {
				localIP = ipNet.IP
				break
			}
		}

		if localIP == nil {
			continue
		}

		addr := &net.UDPAddr{
			IP:   net.ParseIP(OlsrMulticastGroup),
			Port: OlsrPort,
		}
		
		laddr := &net.UDPAddr{
			IP:   localIP,
			Port: 0,
		}

		conn, err := net.DialUDP("udp", laddr, addr)
		if err != nil {
			continue
		}
		
		_, _ = conn.Write(data)
		conn.Close()
	}
}

func (d *Daemon) udpReceiverLoop() {
	gaddr := &net.UDPAddr{
		IP:   net.ParseIP(OlsrMulticastGroup),
		Port: OlsrPort,
	}

	conn, err := net.ListenMulticastUDP("udp", nil, gaddr)
	if err != nil {
		slog.Error("Failed to bind UDP port 698 for receiver, running in virtual simulation mode", "error", err)
		return
	}
	defer conn.Close()

	buf := make([]byte, 65535)
	for {
		select {
		case <-d.ctx.Done():
			return
		default:
			_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second))
			n, raddr, err := conn.ReadFromUDP(buf)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				continue
			}

			metrics.PacketBacklog.Inc()
			d.processReceivedData(buf[:n], raddr.IP)
			metrics.PacketBacklog.Dec()
		}
	}
}

func (d *Daemon) processReceivedData(data []byte, raddr net.IP) {
	pkt, err := olsr.DeserializePacket(data)
	if err != nil {
		metrics.PacketParseFailureTotal.Inc()
		return
	}

	cfg := d.cfgMgr.Get()
	for _, msg := range pkt.Messages {
		if msg.Header.OriginatorAddress.String() == cfg.RouterID {
			continue
		}
		if d.topoMgr.IsDuplicate(msg.Header.OriginatorAddress, msg.Header.SeqNum, msg.Header.Vtime) {
			continue
		}

		switch msg.Header.Type {
		case olsr.MsgTypeHello:
			metrics.HelloRxTotal.Inc()
			if hello, ok := msg.Body.(olsr.HelloMessage); ok {
				d.neighMgr.ProcessHello(d.ctx, raddr, msg.Header.OriginatorAddress, hello, msg.Header.Vtime)
			}
		case olsr.MsgTypeTC:
			metrics.TCRxTotal.Inc()
			if tc, ok := msg.Body.(olsr.TCMessage); ok {
				d.topoMgr.ProcessTC(d.ctx, msg.Header.OriginatorAddress, tc, msg.Header.Vtime)
			}
		case olsr.MsgTypeMID:
			if mid, ok := msg.Body.(olsr.MIDMessage); ok {
				d.topoMgr.ProcessMID(d.ctx, msg.Header.OriginatorAddress, mid, msg.Header.Vtime)
			}
		case olsr.MsgTypeHNA:
			if hna, ok := msg.Body.(olsr.HNAMessage); ok {
				d.hnaMgr.ProcessHNA(d.ctx, msg.Header.OriginatorAddress, hna, msg.Header.Vtime)
			}
		case olsr.MsgTypeSourceClaim:
			if sc, ok := msg.Body.(olsr.SourceClaimMessage); ok {
				d.molsrMgr.ProcessSourceClaim(d.ctx, msg.Header.OriginatorAddress, sc, msg.Header.Vtime)
			}
		case olsr.MsgTypeConfirmParent:
			if cp, ok := msg.Body.(olsr.ConfirmParentMessage); ok {
				d.molsrMgr.ProcessConfirmParent(d.ctx, msg.Header.OriginatorAddress, cp, msg.Header.Vtime)
			}
		}

		if msg.Header.TTL > 1 && msg.Header.Type != olsr.MsgTypeHello && msg.Header.Type != olsr.MsgTypeConfirmParent {
			senderStr := raddr.String()
			isMprOfSender := false
			for _, mpr := range d.neighMgr.GetMPRSelectors() {
				if mpr == senderStr {
					isMprOfSender = true
					break
				}
			}

			if isMprOfSender || msg.Header.Type == olsr.MsgTypeSourceClaim {
				forwardedMsg := msg
				forwardedMsg.Header.TTL--
				forwardedMsg.Header.HopCount++
				
				fwdPkt := &olsr.Packet{
					Header:   pkt.Header,
					Messages: []olsr.Message{forwardedMsg},
				}
				d.broadcastPacket(fwdPkt)
			}
		}
	}
}

type TopologySnapshot struct {
	Timestamp time.Time            `json:"timestamp"`
	Topology  []olsr.TopologyTuple `json:"topology"`
	MIDMap    map[string][]string  `json:"mid_map"`
}

type RouteSnapshot struct {
	Timestamp time.Time                     `json:"timestamp"`
	Unicast   []olsr.Route                  `json:"unicast"`
	Multicast []olsr.MulticastForwardingEntry `json:"multicast"`
}

func (d *Daemon) saveSnapshots() {
	_ = os.MkdirAll(PersistenceDir, 0755)

	topoSnap := TopologySnapshot{
		Timestamp: time.Now(),
		Topology:  d.topoMgr.GetTopology(),
		MIDMap:    d.topoMgr.GetMIDMap(),
	}
	topoData, err := json.MarshalIndent(topoSnap, "", "  ")
	if err == nil {
		_ = os.WriteFile(filepath.Join(PersistenceDir, "topology_snapshot.json"), topoData, 0644)
	}

	routeSnap := RouteSnapshot{
		Timestamp: time.Now(),
		Unicast:   d.spfEngine.GetRoutes(),
		Multicast: d.molsrMgr.GetActiveMFC(),
	}
	routeData, err := json.MarshalIndent(routeSnap, "", "  ")
	if err == nil {
		_ = os.WriteFile(filepath.Join(PersistenceDir, "route_snapshot.json"), routeData, 0644)
	}
}
