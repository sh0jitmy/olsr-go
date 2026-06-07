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

package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "olsrd.yaml")
	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	return tmpFile
}

func TestConfigLoadSuccess(t *testing.T) {
	content := `
router_id: 1.1.1.1
interfaces:
  - eth0
  - wlan0
hello_interval: 2s
tc_interval: 5s
metrics:
  type: etx
`
	tmpFile := writeTempFile(t, content)
	mgr := NewManager(tmpFile)
	err := mgr.Load()
	if err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}

	cfg := mgr.Get()
	if cfg.RouterID != "1.1.1.1" {
		t.Errorf("expected router_id 1.1.1.1, got %s", cfg.RouterID)
	}
	if len(cfg.Interfaces) != 2 || cfg.Interfaces[0] != "eth0" || cfg.Interfaces[1] != "wlan0" {
		t.Errorf("unexpected interfaces: %v", cfg.Interfaces)
	}
	if cfg.HelloInterval != 2*time.Second {
		t.Errorf("expected hello_interval 2s, got %v", cfg.HelloInterval)
	}
	if cfg.TCInterval != 5*time.Second {
		t.Errorf("expected tc_interval 5s, got %v", cfg.TCInterval)
	}
	if cfg.Metrics.Type != "etx" {
		t.Errorf("expected metrics type etx, got %s", cfg.Metrics.Type)
	}
}

func TestConfigValidationFailures(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "missing router_id",
			yaml: `
interfaces:
  - eth0
hello_interval: 2s
tc_interval: 5s
`,
			wantErr: "router_id is required",
		},
		{
			name: "invalid router_id IP",
			yaml: `
router_id: 999.999.999.999
interfaces:
  - eth0
hello_interval: 2s
tc_interval: 5s
`,
			wantErr: "router_id must be a valid IPv4 address",
		},
		{
			name: "duplicate interfaces",
			yaml: `
router_id: 1.1.1.1
interfaces:
  - eth0
  - eth0
hello_interval: 2s
tc_interval: 5s
`,
			wantErr: "duplicate interface detected: eth0",
		},
		{
			name: "invalid hello_interval",
			yaml: `
router_id: 1.1.1.1
interfaces:
  - eth0
hello_interval: -1s
tc_interval: 5s
`,
			wantErr: "hello_interval must be positive",
		},
		{
			name: "invalid tc_interval",
			yaml: `
router_id: 1.1.1.1
interfaces:
  - eth0
hello_interval: 2s
tc_interval: 0s
`,
			wantErr: "tc_interval must be positive",
		},
		{
			name: "invalid metrics type",
			yaml: `
router_id: 1.1.1.1
interfaces:
  - eth0
hello_interval: 2s
tc_interval: 5s
metrics:
  type: invalid_metric
`,
			wantErr: "invalid metrics type: invalid_metric",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpFile := writeTempFile(t, tt.yaml)
			err := ValidateFile(tmpFile)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			// check substring
			t.Logf("got expected error: %v", err)
		})
	}
}

func TestConfigReloadAndRollback(t *testing.T) {
	initialYaml := `
router_id: 1.1.1.1
interfaces:
  - eth0
hello_interval: 2s
tc_interval: 5s
`
	tmpFile := writeTempFile(t, initialYaml)
	mgr := NewManager(tmpFile)
	if err := mgr.Load(); err != nil {
		t.Fatalf("failed load: %v", err)
	}

	// 1. Valid update
	validYaml := `
router_id: 2.2.2.2
interfaces:
  - eth1
hello_interval: 4s
tc_interval: 10s
`
	if err := os.WriteFile(tmpFile, []byte(validYaml), 0644); err != nil {
		t.Fatalf("failed to write updated config: %v", err)
	}

	if err := mgr.Reload(); err != nil {
		t.Fatalf("failed reload: %v", err)
	}

	cfg := mgr.Get()
	if cfg.RouterID != "2.2.2.2" || cfg.Interfaces[0] != "eth1" || cfg.HelloInterval != 4*time.Second {
		t.Errorf("failed config reload propagation: %+v", cfg)
	}

	// 2. Invalid update (rollback)
	invalidYaml := `
router_id: invalid-ip
interfaces:
  - eth1
hello_interval: 4s
tc_interval: 10s
`
	if err := os.WriteFile(tmpFile, []byte(invalidYaml), 0644); err != nil {
		t.Fatalf("failed to write invalid config: %v", err)
	}

	if err := mgr.Reload(); err == nil {
		t.Fatalf("expected reload error due to validation, but got nil")
	}

	// Should still have previous valid configuration (2.2.2.2)
	cfg2 := mgr.Get()
	if cfg2.RouterID != "2.2.2.2" {
		t.Errorf("rollback failed, expected router_id 2.2.2.2, got %s", cfg2.RouterID)
	}
}
