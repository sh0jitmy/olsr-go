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
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"time"
)

// Message Type constants
const (
	MsgTypeHello        uint8 = 1
	MsgTypeTC           uint8 = 2
	MsgTypeMID          uint8 = 3
	MsgTypeHNA          uint8 = 4
	MsgTypeSourceClaim  uint8 = 108 // MOLSR
	MsgTypeConfirmParent uint8 = 109 // MOLSR
)

// Link Type constants
const (
	LinkTypeUnspec uint8 = 0
	LinkTypeAsym   uint8 = 1
	LinkTypeSym    uint8 = 2
	LinkTypeLost   uint8 = 3
)

// Neighbor Type constants
const (
	NeighTypeNotNeigh uint8 = 0
	NeighTypeSym      uint8 = 1
	NeighTypeMPR      uint8 = 2
)

// Willingness constants
const (
	WillNever   uint8 = 0
	WillLow     uint8 = 1
	WillDefault uint8 = 3
	WillHigh    uint8 = 6
	WillAlways  uint8 = 7
)

// Decode Vtime/Htime value based on RFC 3626 Section 18.3
func DecodeTimeVal(val uint8) time.Duration {
	a := val >> 4
	b := val & 0x0f
	t := 0.06 * (1.0 + float64(a)/16.0)
	for i := uint8(0); i < b; i++ {
		t *= 10.0
	}
	return time.Duration(t * float64(time.Second))
}

// Encode Vtime/Htime value based on RFC 3626 Section 18.3
func EncodeTimeVal(d time.Duration) uint8 {
	sec := float64(d) / float64(time.Second)
	var bestVal uint8 = 0
	var minDiff float64 = -1
	for a := uint8(0); a < 16; a++ {
		for b := uint8(0); b < 16; b++ {
			t := 0.06 * (1.0 + float64(a)/16.0)
			for i := uint8(0); i < b; i++ {
				t *= 10.0
			}
			diff := t - sec
			if diff < 0 {
				diff = -diff
			}
			if minDiff < 0 || diff < minDiff {
				minDiff = diff
				bestVal = (a << 4) | b
			}
		}
	}
	return bestVal
}

// Packets and Message structures

type PacketHeader struct {
	PacketLength int    // Includes header
	PacketSeqNum uint16
}

type MessageHeader struct {
	Type              uint8
	Vtime             time.Duration
	OriginatorAddress net.IP
	TTL               uint8
	HopCount          uint8
	SeqNum            uint16
}

type HelloLinkMessage struct {
	LinkCode          uint8
	NeighborAddresses []net.IP
}

type HelloMessage struct {
	Htime        time.Duration
	Willingness  uint8
	LinkMessages []HelloLinkMessage
}

type TCMessage struct {
	ANSN              uint16
	NeighborAddresses []net.IP
}

type MIDMessage struct {
	Addresses []net.IP
}

type HNAAssociation struct {
	Address net.IP
	Netmask net.IPNet
}

type HNAMessage struct {
	Associations []HNAAssociation
}

type SourceClaimMessage struct {
	SourceIP net.IP
	GroupID  net.IP
}

type ConfirmParentMessage struct {
	SourceIP net.IP
	GroupID  net.IP
	ParentIP net.IP
}

type Message struct {
	Header MessageHeader
	Body   interface{} // One of HelloMessage, TCMessage, MIDMessage, HNAMessage, SourceClaimMessage, ConfirmParentMessage
}

type Packet struct {
	Header   PacketHeader
	Messages []Message
}

// Serialization & Deserialization

// SerializePacket converts a Packet struct to binary payload
func SerializePacket(p *Packet) ([]byte, error) {
	buf := new(bytes.Buffer)

	// Packet Header placeholders
	// Length (2 bytes), SeqNum (2 bytes)
	if err := binary.Write(buf, binary.BigEndian, uint16(0)); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.BigEndian, p.Header.PacketSeqNum); err != nil {
		return nil, err
	}

	for _, msg := range p.Messages {
		msgBuf := new(bytes.Buffer)
		
		// Message Type
		if err := msgBuf.WriteByte(msg.Header.Type); err != nil {
			return nil, err
		}
		// Vtime
		if err := msgBuf.WriteByte(EncodeTimeVal(msg.Header.Vtime)); err != nil {
			return nil, err
		}
		// Message Size placeholder (2 bytes)
		if err := binary.Write(msgBuf, binary.BigEndian, uint16(0)); err != nil {
			return nil, err
		}
		// Originator Address (IPv4 4 bytes)
		origIPv4 := msg.Header.OriginatorAddress.To4()
		if origIPv4 == nil {
			return nil, fmt.Errorf("invalid IPv4 address for originator: %v", msg.Header.OriginatorAddress)
		}
		if _, err := msgBuf.Write(origIPv4); err != nil {
			return nil, err
		}
		// TTL
		if err := msgBuf.WriteByte(msg.Header.TTL); err != nil {
			return nil, err
		}
		// HopCount
		if err := msgBuf.WriteByte(msg.Header.HopCount); err != nil {
			return nil, err
		}
		// Message Sequence Number
		if err := binary.Write(msgBuf, binary.BigEndian, msg.Header.SeqNum); err != nil {
			return nil, err
		}

		// Message Body
		switch body := msg.Body.(type) {
		case HelloMessage:
			// Reserved (16 bits)
			if err := binary.Write(msgBuf, binary.BigEndian, uint16(0)); err != nil {
				return nil, err
			}
			// Htime
			if err := msgBuf.WriteByte(EncodeTimeVal(body.Htime)); err != nil {
				return nil, err
			}
			// Willingness
			if err := msgBuf.WriteByte(body.Willingness); err != nil {
				return nil, err
			}
			for _, lm := range body.LinkMessages {
				linkBuf := new(bytes.Buffer)
				// Link Code
				if err := linkBuf.WriteByte(lm.LinkCode); err != nil {
					return nil, err
				}
				// Reserved
				if err := linkBuf.WriteByte(0); err != nil {
					return nil, err
				}
				// Link Message Size (2 bytes) placeholder
				if err := binary.Write(linkBuf, binary.BigEndian, uint16(0)); err != nil {
					return nil, err
				}
				// Neighbor Addresses
				for _, addr := range lm.NeighborAddresses {
					addrIPv4 := addr.To4()
					if addrIPv4 == nil {
						return nil, fmt.Errorf("invalid IPv4 address for neighbor: %v", addr)
					}
					if _, err := linkBuf.Write(addrIPv4); err != nil {
						return nil, err
					}
				}
				linkBytes := linkBuf.Bytes()
				if len(linkBytes) > 65535 {
					return nil, fmt.Errorf("link message size exceeds maximum uint16 size")
				}
				// Fill Link Message Size (which is the length of Link Message header + addresses)
				//nolint:gosec // G115: length is pre-validated to be <= 65535
				binary.BigEndian.PutUint16(linkBytes[2:4], uint16(len(linkBytes)))
				if _, err := msgBuf.Write(linkBytes); err != nil {
					return nil, err
				}
			}

		case TCMessage:
			// ANSN (2 bytes)
			if err := binary.Write(msgBuf, binary.BigEndian, body.ANSN); err != nil {
				return nil, err
			}
			// Reserved (2 bytes)
			if err := binary.Write(msgBuf, binary.BigEndian, uint16(0)); err != nil {
				return nil, err
			}
			for _, addr := range body.NeighborAddresses {
				addrIPv4 := addr.To4()
				if addrIPv4 == nil {
					return nil, fmt.Errorf("invalid IPv4 address: %v", addr)
				}
				if _, err := msgBuf.Write(addrIPv4); err != nil {
					return nil, err
				}
			}

		case MIDMessage:
			for _, addr := range body.Addresses {
				addrIPv4 := addr.To4()
				if addrIPv4 == nil {
					return nil, fmt.Errorf("invalid IPv4 address: %v", addr)
				}
				if _, err := msgBuf.Write(addrIPv4); err != nil {
					return nil, err
				}
			}

		case HNAMessage:
			for _, assoc := range body.Associations {
				addrIPv4 := assoc.Address.To4()
				maskIPv4 := net.IP(assoc.Netmask.Mask).To4()
				if addrIPv4 == nil || maskIPv4 == nil {
					return nil, fmt.Errorf("invalid IPv4 address/mask: %v/%v", assoc.Address, assoc.Netmask.Mask)
				}
				if _, err := msgBuf.Write(addrIPv4); err != nil {
					return nil, err
				}
				if _, err := msgBuf.Write(maskIPv4); err != nil {
					return nil, err
				}
			}

		case SourceClaimMessage:
			srcIPv4 := body.SourceIP.To4()
			grpIPv4 := body.GroupID.To4()
			if srcIPv4 == nil || grpIPv4 == nil {
				return nil, fmt.Errorf("invalid IPv4 multicast source/group: %v/%v", body.SourceIP, body.GroupID)
			}
			if _, err := msgBuf.Write(srcIPv4); err != nil {
				return nil, err
			}
			if _, err := msgBuf.Write(grpIPv4); err != nil {
				return nil, err
			}

		case ConfirmParentMessage:
			srcIPv4 := body.SourceIP.To4()
			grpIPv4 := body.GroupID.To4()
			parentIPv4 := body.ParentIP.To4()
			if srcIPv4 == nil || grpIPv4 == nil || parentIPv4 == nil {
				return nil, fmt.Errorf("invalid IPv4 Source/Group/Parent: %v/%v/%v", body.SourceIP, body.GroupID, body.ParentIP)
			}
			if _, err := msgBuf.Write(srcIPv4); err != nil {
				return nil, err
			}
			if _, err := msgBuf.Write(grpIPv4); err != nil {
				return nil, err
			}
			if _, err := msgBuf.Write(parentIPv4); err != nil {
				return nil, err
			}

		default:
			return nil, fmt.Errorf("unsupported message body type: %T", msg.Body)
		}

		msgBytes := msgBuf.Bytes()
		if len(msgBytes) > 65535 {
			return nil, fmt.Errorf("message size exceeds maximum uint16 size")
		}
		// Fill Message Size (length of message header + body)
		//nolint:gosec // G115: length is pre-validated to be <= 65535
		binary.BigEndian.PutUint16(msgBytes[2:4], uint16(len(msgBytes)))
		if _, err := buf.Write(msgBytes); err != nil {
			return nil, err
		}
	}

	packetBytes := buf.Bytes()
	if len(packetBytes) > 65535 {
		return nil, fmt.Errorf("packet size exceeds maximum uint16 size")
	}
	// Fill Packet Length
	//nolint:gosec // G115: length is pre-validated to be <= 65535
	binary.BigEndian.PutUint16(packetBytes[0:2], uint16(len(packetBytes)))
	return packetBytes, nil
}

// DeserializePacket converts binary payload back to Packet struct
func DeserializePacket(data []byte) (*Packet, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("packet data too short")
	}

	pktLen := binary.BigEndian.Uint16(data[0:2])
	pktSeq := binary.BigEndian.Uint16(data[2:4])

	if pktLen < 4 {
		return nil, fmt.Errorf("invalid packet length %d, min is 4", pktLen)
	}

	if len(data) < int(pktLen) {
		return nil, fmt.Errorf("packet length mismatch: declared %d, actual %d", pktLen, len(data))
	}

	// Read messages
	offset := 4
	messages := make([]Message, 0)
	for offset < int(pktLen) {
		if len(data)-offset < 12 { // Min msg header size is 12 bytes
			return nil, fmt.Errorf("incomplete message header at offset %d", offset)
		}

		msgType := data[offset]
		vtimeVal := data[offset+1]
		msgSize := binary.BigEndian.Uint16(data[offset+2 : offset+4])
		originator := cloneIP(data[offset+4 : offset+8])
		ttl := data[offset+8]
		hopCount := data[offset+9]
		msgSeq := binary.BigEndian.Uint16(data[offset+10 : offset+12])

		if offset+int(msgSize) > int(pktLen) {
			return nil, fmt.Errorf("message size %d exceeds packet length at offset %d", msgSize, offset)
		}

		msgBodyData := data[offset+12 : offset+int(msgSize)]
		var body interface{}

		switch msgType {
		case MsgTypeHello:
			if len(msgBodyData) < 4 {
				return nil, fmt.Errorf("invalid HELLO message body length")
			}
			htimeVal := msgBodyData[2]
			will := msgBodyData[3]

			linkMsgs := make([]HelloLinkMessage, 0)
			linkOffset := 4
			for linkOffset < len(msgBodyData) {
				if len(msgBodyData)-linkOffset < 4 {
					return nil, fmt.Errorf("invalid HelloLinkMessage header")
				}
				linkCode := msgBodyData[linkOffset]
				linkMsgSize := binary.BigEndian.Uint16(msgBodyData[linkOffset+2 : linkOffset+4])

				if linkOffset+int(linkMsgSize) > len(msgBodyData) {
					return nil, fmt.Errorf("HelloLinkMessage size %d exceeds HELLO body", linkMsgSize)
				}

				addrData := msgBodyData[linkOffset+4 : linkOffset+int(linkMsgSize)]
				if len(addrData)%4 != 0 {
					return nil, fmt.Errorf("invalid addresses length in Link Message: %d", len(addrData))
				}

				addrs := make([]net.IP, len(addrData)/4)
				for i := 0; i < len(addrs); i++ {
					addrs[i] = cloneIP(addrData[i*4 : (i+1)*4])
				}

				linkMsgs = append(linkMsgs, HelloLinkMessage{
					LinkCode:          linkCode,
					NeighborAddresses: addrs,
				})
				linkOffset += int(linkMsgSize)
			}

			body = HelloMessage{
				Htime:        DecodeTimeVal(htimeVal),
				Willingness:  will,
				LinkMessages: linkMsgs,
			}

		case MsgTypeTC:
			if len(msgBodyData) < 4 {
				return nil, fmt.Errorf("invalid TC message body length")
			}
			ansn := binary.BigEndian.Uint16(msgBodyData[0:2])
			addrData := msgBodyData[4:]
			if len(addrData)%4 != 0 {
				return nil, fmt.Errorf("invalid addresses length in TC Message: %d", len(addrData))
			}
			addrs := make([]net.IP, len(addrData)/4)
			for i := 0; i < len(addrs); i++ {
				addrs[i] = cloneIP(addrData[i*4 : (i+1)*4])
			}
			body = TCMessage{
				ANSN:              ansn,
				NeighborAddresses: addrs,
			}

		case MsgTypeMID:
			if len(msgBodyData)%4 != 0 {
				return nil, fmt.Errorf("invalid addresses length in MID Message: %d", len(msgBodyData))
			}
			addrs := make([]net.IP, len(msgBodyData)/4)
			for i := 0; i < len(addrs); i++ {
				addrs[i] = cloneIP(msgBodyData[i*4 : (i+1)*4])
			}
			body = MIDMessage{
				Addresses: addrs,
			}

		case MsgTypeHNA:
			if len(msgBodyData)%8 != 0 {
				return nil, fmt.Errorf("invalid associations length in HNA Message: %d", len(msgBodyData))
			}
			assocs := make([]HNAAssociation, len(msgBodyData)/8)
			for i := 0; i < len(assocs); i++ {
				addr := cloneIP(msgBodyData[i*8 : i*8+4])
				mask := cloneMask(msgBodyData[i*8+4 : i*8+8])
				assocs[i] = HNAAssociation{
					Address: addr,
					Netmask: net.IPNet{
						IP:   addr,
						Mask: mask,
					},
				}
			}
			body = HNAMessage{
				Associations: assocs,
			}

		case MsgTypeSourceClaim:
			if len(msgBodyData) < 8 {
				return nil, fmt.Errorf("invalid SOURCE CLAIM message body length")
			}
			body = SourceClaimMessage{
				SourceIP: cloneIP(msgBodyData[0:4]),
				GroupID:  cloneIP(msgBodyData[4:8]),
			}

		case MsgTypeConfirmParent:
			if len(msgBodyData) < 12 {
				return nil, fmt.Errorf("invalid CONFIRM PARENT message body length")
			}
			body = ConfirmParentMessage{
				SourceIP: cloneIP(msgBodyData[0:4]),
				GroupID:  cloneIP(msgBodyData[4:8]),
				ParentIP: cloneIP(msgBodyData[8:12]),
			}

		default:
			// To keep parsing other messages in case of unknown types, we can just skip or save as raw bytes
			body = msgBodyData
		}

		messages = append(messages, Message{
			Header: MessageHeader{
				Type:              msgType,
				Vtime:             DecodeTimeVal(vtimeVal),
				OriginatorAddress: originator,
				TTL:               ttl,
				HopCount:          hopCount,
				SeqNum:            msgSeq,
			},
			Body: body,
		})

		offset += int(msgSize)
	}

	return &Packet{
		Header: PacketHeader{
			PacketLength: int(pktLen),
			PacketSeqNum: pktSeq,
		},
		Messages: messages,
	}, nil
}

func cloneIP(b []byte) net.IP {
	if len(b) == 0 {
		return nil
	}
	res := make(net.IP, len(b))
	copy(res, b)
	return res
}

func cloneMask(b []byte) net.IPMask {
	if len(b) == 0 {
		return nil
	}
	res := make(net.IPMask, len(b))
	copy(res, b)
	return res
}
