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
	ZapiMarker        uint8  = 0xfe
	ZapiVersion6      uint8  = 6
	ZebraIpmrRouteAdd uint16 = 59
	ZebraIpmrRouteDel uint16 = 60
)

// BuildIpmrMessage constructs a raw ZAPI v6 binary packet for multicast route additions/deletions.
func BuildIpmrMessage(cmd uint16, srcIP, grpIP net.IP, iif int, oifs []int) ([]byte, error) {
	srcIPv4 := srcIP.To4()
	grpIPv4 := grpIP.To4()
	if srcIPv4 == nil || grpIPv4 == nil {
		return nil, fmt.Errorf("IPMR route requires IPv4 addresses")
	}

	buf := new(bytes.Buffer)

	if err := writeIpmrHeader(buf, cmd); err != nil {
		return nil, err
	}

	if err := writeIpmrBody(buf, srcIPv4, grpIPv4, iif, oifs); err != nil {
		return nil, err
	}

	// Fill length header
	data := buf.Bytes()
	if len(data) > 65535 {
		return nil, fmt.Errorf("multicast route message size exceeds maximum uint16 size")
	}
	//nolint:gosec // G115: length is pre-validated to be <= 65535
	binary.BigEndian.PutUint16(data[0:2], uint16(len(data)))

	return data, nil
}

func writeIpmrHeader(buf *bytes.Buffer, cmd uint16) error {
	// Size (uint16 placeholder, filled later)
	if err := binary.Write(buf, binary.BigEndian, uint16(0)); err != nil {
		return err
	}
	// Marker (uint8)
	if err := buf.WriteByte(ZapiMarker); err != nil {
		return err
	}
	// Version (uint8)
	if err := buf.WriteByte(ZapiVersion6); err != nil {
		return err
	}
	// VRF ID (uint32)
	if err := binary.Write(buf, binary.BigEndian, uint32(0)); err != nil {
		return err
	}
	// Command (uint16)
	if err := binary.Write(buf, binary.BigEndian, cmd); err != nil {
		return err
	}
	return nil
}

func writeIpmrBody(buf *bytes.Buffer, srcIPv4, grpIPv4 net.IP, iif int, oifs []int) error {
	// Family (uint8, IPv4 = 2)
	if err := buf.WriteByte(uint8(2)); err != nil {
		return err
	}
	// Source IP (4 bytes)
	if _, err := buf.Write(srcIPv4); err != nil {
		return err
	}
	// Group IP (4 bytes)
	if _, err := buf.Write(grpIPv4); err != nil {
		return err
	}
	// IIF (uint32)
	if iif < 0 {
		return fmt.Errorf("invalid IIF index: %d", iif)
	}
	//nolint:gosec // G115: iif is pre-validated to be >= 0
	if err := binary.Write(buf, binary.BigEndian, uint32(iif)); err != nil {
		return err
	}
	// Num OIFs (uint32)
	if len(oifs) > 65535 {
		return fmt.Errorf("invalid OIFs count: %d", len(oifs))
	}
	//nolint:gosec // G115: length is pre-validated to be <= 65535
	if err := binary.Write(buf, binary.BigEndian, uint32(len(oifs))); err != nil {
		return err
	}
	// OIF array (each: interface index(uint32) + TTL(uint32))
	for _, oif := range oifs {
		if oif < 0 {
			return fmt.Errorf("invalid OIF index: %d", oif)
		}
		//nolint:gosec // G115: oif is pre-validated to be >= 0
		if err := binary.Write(buf, binary.BigEndian, uint32(oif)); err != nil {
			return err
		}
		// Default multicast TTL = 1 for mesh link forwarding
		if err := binary.Write(buf, binary.BigEndian, uint32(1)); err != nil {
			return err
		}
	}
	return nil
}
