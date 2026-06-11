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

//go:build linux

package mroute

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNFTablesManagerLinux(t *testing.T) {
	mockContent := `IP address       HW type     Flags       HW address            Mask     Device
10.10.1.20       0x1         0x2         02:42:0a:0a:01:14     *        eth0
`
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "mock_arp")
	if err := os.WriteFile(tmpFile, []byte(mockContent), 0600); err != nil {
		t.Fatalf("failed to write mock arp: %v", err)
	}

	origPath := arpPath
	arpPath = tmpFile
	defer func() { arpPath = origPath }()

	var capturedCmds []string
	mockExec := func(name string, arg ...string) *exec.Cmd {
		capturedCmds = append(capturedCmds, name+" "+strings.Join(arg, " "))
		return exec.Command("true")
	}

	mgr := NewNFTablesManager().(*NFTablesManager)
	mgr.execCmd = mockExec

	ctx := context.Background()
	if err := mgr.Start(ctx, []string{"wlan0"}); err != nil {
		t.Fatalf("failed to start: %v", err)
	}

	if err := mgr.OnMulticastRouteAdd("10.10.1.10", "239.2.2.2", "10.10.1.20"); err != nil {
		t.Fatalf("failed to add route: %v", err)
	}

	// Verify that commands were captured
	foundAccept := false
	foundDrop := false
	for _, cmd := range capturedCmds {
		if strings.Contains(cmd, "ether saddr 02:42:0a:0a:01:14 accept") {
			foundAccept = true
		}
		if strings.Contains(cmd, "239.2.2.2 drop") {
			foundDrop = true
		}
	}
	if !foundAccept || !foundDrop {
		t.Errorf("expected to find accept and drop commands in: %v", capturedCmds)
	}

	capturedCmds = nil
	if err := mgr.OnMulticastRouteDelete("10.10.1.10", "239.2.2.2"); err != nil {
		t.Fatalf("failed to delete route: %v", err)
	}

	foundFlush := false
	for _, cmd := range capturedCmds {
		if strings.Contains(cmd, "flush chain inet molsr_filter prerouting") {
			foundFlush = true
		}
	}
	if !foundFlush {
		t.Errorf("expected to find flush command in: %v", capturedCmds)
	}

	if err := mgr.Stop(); err != nil {
		t.Fatalf("failed to stop: %v", err)
	}
}
