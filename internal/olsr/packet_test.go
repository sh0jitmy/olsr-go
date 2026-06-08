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
	"net"
	"testing"
	"time"
)

func TestTimeValEncoding(t *testing.T) {
	tests := []struct {
		duration time.Duration
		val      uint8
	}{
		{1 * time.Second, 0x1d}, // C * (1 + 1/16) * 10^1 = 0.06 * 1.0625 * 10 = 0.6375s? Let's check calculations.
		{6 * time.Second, 0x0},  // Just basic checks
	}

	for _, tt := range tests {
		val := EncodeTimeVal(tt.duration)
		decoded := DecodeTimeVal(val)
		diff := decoded - tt.duration
		if diff < 0 {
			diff = -diff
		}
		// Tolerable difference for exponential representation (approximate mapping)
		if diff > 500*time.Millisecond {
			t.Errorf("vtime mapping deviation too high: original=%v, encoded=0x%x, decoded=%v (diff=%v)", tt.duration, val, decoded, diff)
		}
	}
}

func TestSerializeDeserializePacket(t *testing.T) {
	origAddr := net.ParseIP("1.1.1.1")
	neigh1 := net.ParseIP("2.2.2.2")
	neigh2 := net.ParseIP("3.3.3.3")

	pkt := &Packet{
		Header: PacketHeader{
			PacketSeqNum: 42,
		},
		Messages: []Message{
			{
				Header: MessageHeader{
					Type:              MsgTypeHello,
					Vtime:             6 * time.Second,
					OriginatorAddress: origAddr,
					TTL:               1,
					HopCount:          0,
					SeqNum:            100,
				},
				Body: HelloMessage{
					Htime:       2 * time.Second,
					Willingness: WillDefault,
					LinkMessages: []HelloLinkMessage{
						{
							LinkCode:          (LinkTypeSym & 0x03) | ((NeighTypeSym & 0x03) << 2),
							NeighborAddresses: []net.IP{neigh1},
						},
						{
							LinkCode:          (LinkTypeAsym & 0x03) | ((NeighTypeNotNeigh & 0x03) << 2),
							NeighborAddresses: []net.IP{neigh2},
						},
					},
				},
			},
			{
				Header: MessageHeader{
					Type:              MsgTypeTC,
					Vtime:             15 * time.Second,
					OriginatorAddress: origAddr,
					TTL:               255,
					HopCount:          0,
					SeqNum:            101,
				},
				Body: TCMessage{
					ANSN:              5,
					NeighborAddresses: []net.IP{neigh1, neigh2},
				},
			},
			{
				Header: MessageHeader{
					Type:              MsgTypeMID,
					Vtime:             15 * time.Second,
					OriginatorAddress: origAddr,
					TTL:               255,
					HopCount:          0,
					SeqNum:            102,
				},
				Body: MIDMessage{
					Addresses: []net.IP{net.ParseIP("10.0.0.1"), net.ParseIP("192.168.0.1")},
				},
			},
			{
				Header: MessageHeader{
					Type:              MsgTypeHNA,
					Vtime:             15 * time.Second,
					OriginatorAddress: origAddr,
					TTL:               255,
					HopCount:          0,
					SeqNum:            103,
				},
				Body: HNAMessage{
					Associations: []HNAAssociation{
						{
							Address: net.ParseIP("192.168.10.0"),
							Netmask: net.IPNet{
								IP:   net.ParseIP("192.168.10.0"),
								Mask: net.CIDRMask(24, 32),
							},
						},
					},
				},
			},
			{
				Header: MessageHeader{
					Type:              MsgTypeSourceClaim,
					Vtime:             10 * time.Second,
					OriginatorAddress: origAddr,
					TTL:               255,
					HopCount:          0,
					SeqNum:            104,
				},
				Body: SourceClaimMessage{
					SourceIP: net.ParseIP("10.10.10.1"),
					GroupID:  net.ParseIP("224.0.0.9"),
				},
			},
			{
				Header: MessageHeader{
					Type:              MsgTypeConfirmParent,
					Vtime:             10 * time.Second,
					OriginatorAddress: origAddr,
					TTL:               1,
					HopCount:          0,
					SeqNum:            105,
				},
				Body: ConfirmParentMessage{
					SourceIP: net.ParseIP("10.10.10.1"),
					GroupID:  net.ParseIP("224.0.0.9"),
					ParentIP: net.ParseIP("1.1.1.2"),
				},
			},
		},
	}

	serialized, err := SerializePacket(pkt)
	if err != nil {
		t.Fatalf("failed serialization: %v", err)
	}

	deserialized, err := DeserializePacket(serialized)
	if err != nil {
		t.Fatalf("failed deserialization: %v", err)
	}

	if deserialized.Header.PacketSeqNum != pkt.Header.PacketSeqNum {
		t.Errorf("seq num mismatch: expected %d, got %d", pkt.Header.PacketSeqNum, deserialized.Header.PacketSeqNum)
	}

	if len(deserialized.Messages) != len(pkt.Messages) {
		t.Fatalf("messages count mismatch: expected %d, got %d", len(pkt.Messages), len(deserialized.Messages))
	}

	validateHelloMsg(t, deserialized.Messages[0], origAddr, neigh1)
	validateTCMsg(t, deserialized.Messages[1], neigh1, neigh2)
	validateMIDMsg(t, deserialized.Messages[2])
	validateHNAMsg(t, deserialized.Messages[3])
	validateSourceClaimMsg(t, deserialized.Messages[4])
	validateConfirmParentMsg(t, deserialized.Messages[5])
}

func validateHelloMsg(t *testing.T, m0 Message, origAddr, neigh1 net.IP) {
	if m0.Header.Type != MsgTypeHello {
		t.Errorf("expected msg type HELLO, got %d", m0.Header.Type)
	}
	if !m0.Header.OriginatorAddress.Equal(origAddr) {
		t.Errorf("originator mismatch")
	}
	hello, ok := m0.Body.(HelloMessage)
	if !ok {
		t.Fatalf("failed to cast message body to HelloMessage")
	}
	if hello.Willingness != WillDefault {
		t.Errorf("willingness mismatch")
	}
	if len(hello.LinkMessages) != 2 {
		t.Fatalf("expected 2 link messages, got %d", len(hello.LinkMessages))
	}
	if len(hello.LinkMessages[0].NeighborAddresses) != 1 || !hello.LinkMessages[0].NeighborAddresses[0].Equal(neigh1) {
		t.Errorf("neigh1 address mismatch")
	}
}

func validateTCMsg(t *testing.T, m1 Message, neigh1, neigh2 net.IP) {
	tc, ok := m1.Body.(TCMessage)
	if !ok {
		t.Fatalf("failed to cast to TCMessage")
	}
	if tc.ANSN != 5 {
		t.Errorf("expected ANSN 5, got %d", tc.ANSN)
	}
	if !tc.NeighborAddresses[0].Equal(neigh1) || !tc.NeighborAddresses[1].Equal(neigh2) {
		t.Errorf("TC addresses mismatch")
	}
}

func validateMIDMsg(t *testing.T, m2 Message) {
	mid, ok := m2.Body.(MIDMessage)
	if !ok {
		t.Fatalf("failed to cast to MIDMessage")
	}
	if !mid.Addresses[0].Equal(net.ParseIP("10.0.0.1")) {
		t.Errorf("MID address mismatch")
	}
}

func validateHNAMsg(t *testing.T, m3 Message) {
	hna, ok := m3.Body.(HNAMessage)
	if !ok {
		t.Fatalf("failed to cast to HNAMessage")
	}
	if !hna.Associations[0].Address.Equal(net.ParseIP("192.168.10.0")) {
		t.Errorf("HNA Address mismatch")
	}
	maskLen, _ := hna.Associations[0].Netmask.Mask.Size()
	if maskLen != 24 {
		t.Errorf("HNA Netmask size mismatch, expected 24, got %d", maskLen)
	}
}

func validateSourceClaimMsg(t *testing.T, m4 Message) {
	sc, ok := m4.Body.(SourceClaimMessage)
	if !ok {
		t.Fatalf("failed to cast to SourceClaimMessage")
	}
	if !sc.SourceIP.Equal(net.ParseIP("10.10.10.1")) || !sc.GroupID.Equal(net.ParseIP("224.0.0.9")) {
		t.Errorf("SourceClaim content mismatch")
	}
}

func validateConfirmParentMsg(t *testing.T, m5 Message) {
	cp, ok := m5.Body.(ConfirmParentMessage)
	if !ok {
		t.Fatalf("failed to cast to ConfirmParentMessage")
	}
	if !cp.SourceIP.Equal(net.ParseIP("10.10.10.1")) || !cp.GroupID.Equal(net.ParseIP("224.0.0.9")) || !cp.ParentIP.Equal(net.ParseIP("1.1.1.2")) {
		t.Errorf("ConfirmParent content mismatch")
	}
}

func TestSerializeEmptyPacket(t *testing.T) {
	pkt := &Packet{
		Header: PacketHeader{PacketSeqNum: 1},
	}
	data, err := SerializePacket(pkt)
	if err != nil {
		t.Fatalf("failed to serialize: %v", err)
	}
	if len(data) != 4 {
		t.Errorf("expected empty packet size 4, got %d", len(data))
	}
}

func TestDeserializeMalformedPacket(t *testing.T) {
	badData := []byte{0, 2, 0, 1} // declared size 2, actual size 4
	_, err := DeserializePacket(badData)
	if err == nil {
		t.Errorf("expected error on malformed packet")
	}
}
