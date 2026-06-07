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
	"encoding/binary"
	"fmt"
	"net"
)

const (
	ZapiHeaderSize int = 10 // v6 header: size(2) + marker(1) + version(1) + vrf_id(4) + command(2)
)

// ZAPI Commands for modern FRRouting (v6)
const (
	ZapiMarker               uint8  = 0xfe
	ZapiVersion6             uint8  = 6
	ZebraIpmrRouteAdd        uint16 = 59
	ZebraIpmrRouteDel        uint16 = 60
)

// BuildIpmrMessage constructs a raw ZAPI v6 binary packet for multicast route additions/deletions.
func BuildIpmrMessage(cmd uint16, srcIP, grpIP net.IP, iif int, oifs []int) ([]byte, error) {
	srcIPv4 := srcIP.To4()
	grpIPv4 := grpIP.To4()
	if srcIPv4 == nil || grpIPv4 == nil {
		return nil, fmt.Errorf("IPMR route requires IPv4 addresses")
	}

	buf := new(bytes.Buffer)

	// --- 1. ZAPI Header ---
	// Size (uint16 placeholder, filled later)
	if err := binary.Write(buf, binary.BigEndian, uint16(0)); err != nil {
		return nil, err
	}
	// Marker (uint8)
	if err := buf.WriteByte(ZapiMarker); err != nil {
		return nil, err
	}
	// Version (uint8)
	if err := buf.WriteByte(ZapiVersion6); err != nil {
		return nil, err
	}
	// VRF ID (uint32)
	if err := binary.Write(buf, binary.BigEndian, uint32(0)); err != nil {
		return nil, err
	}
	// Command (uint16)
	if err := binary.Write(buf, binary.BigEndian, cmd); err != nil {
		return nil, err
	}

	// --- 2. ZAPI IPMR Body ---
	// Family (uint8, IPv4 = 2)
	if err := buf.WriteByte(uint8(2)); err != nil {
		return nil, err
	}
	// Source IP (4 bytes)
	if _, err := buf.Write(srcIPv4); err != nil {
		return nil, err
	}
	// Group IP (4 bytes)
	if _, err := buf.Write(grpIPv4); err != nil {
		return nil, err
	}
	// IIF (uint32)
	if err := binary.Write(buf, binary.BigEndian, uint32(iif)); err != nil {
		return nil, err
	}
	// Num OIFs (uint32)
	if err := binary.Write(buf, binary.BigEndian, uint32(len(oifs))); err != nil {
		return nil, err
	}
	// OIF array (each: interface index(uint32) + TTL(uint32))
	for _, oif := range oifs {
		if err := binary.Write(buf, binary.BigEndian, uint32(oif)); err != nil {
			return nil, err
		}
		// Default multicast TTL = 1 for mesh link forwarding
		if err := binary.Write(buf, binary.BigEndian, uint32(1)); err != nil {
			return nil, err
		}
	}

	// Fill length header
	data := buf.Bytes()
	binary.BigEndian.PutUint16(data[0:2], uint16(len(data)))

	return data, nil
}
