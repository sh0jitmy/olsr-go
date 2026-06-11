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
	"os"
	"path/filepath"
	"testing"
)

func TestResolveMACFromARP(t *testing.T) {
	mockContent := `IP address       HW type     Flags       HW address            Mask     Device
10.10.1.20       0x1         0x2         02:42:0a:0a:01:14     *        eth0
10.10.1.30       0x1         0x2         00:00:00:00:00:00     *        eth0
`
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "mock_arp")
	if err := os.WriteFile(tmpFile, []byte(mockContent), 0600); err != nil {
		t.Fatalf("failed to write mock arp: %v", err)
	}

	// Save original and restore
	origPath := arpPath
	arpPath = tmpFile
	defer func() { arpPath = origPath }()

	// Test case 1: Successful resolution
	mac, err := ResolveMACFromARP("10.10.1.20")
	if err != nil {
		t.Fatalf("unexpected error resolving MAC: %v", err)
	}
	if mac != "02:42:0a:0a:01:14" {
		t.Errorf("expected 02:42:0a:0a:01:14, got %s", mac)
	}

	// Test case 2: Unresolved MAC (00:00:00:00:00:00)
	_, err = ResolveMACFromARP("10.10.1.30")
	if err == nil {
		t.Errorf("expected error for unresolved MAC, got nil")
	}

	// Test case 3: Non-existent IP
	_, err = ResolveMACFromARP("10.10.1.99")
	if err == nil {
		t.Errorf("expected error for non-existent IP, got nil")
	}
}
