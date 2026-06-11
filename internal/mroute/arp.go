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

package mroute

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

var arpPath = "/proc/net/arp"

// ResolveMACFromARP reads /proc/net/arp and returns the MAC address associated with the target IP address.
func ResolveMACFromARP(ipAddr string) (string, error) {
	file, err := os.Open(arpPath)
	if err != nil {
		return "", fmt.Errorf("failed to open /proc/net/arp: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	// Skip header line
	if scanner.Scan() {
		_ = scanner.Text()
	}

	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) >= 4 {
			ip := fields[0]
			mac := fields[3]
			if ip == ipAddr {
				// Verify it's a valid MAC (not 00:00:00:00:00:00)
				if mac != "00:00:00:00:00:00" && mac != "" {
					return mac, nil
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("error reading /proc/net/arp: %w", err)
	}

	return "", fmt.Errorf("MAC address not found in ARP table for IP: %s", ipAddr)
}
