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
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type MetricsConfig struct {
	Type string `yaml:"type"`
}

type Config struct {
	RouterID      string        `yaml:"router_id"`
	Interfaces    []string      `yaml:"interfaces"`
	HelloInterval time.Duration `yaml:"hello_interval"`
	TCInterval    time.Duration `yaml:"tc_interval"`
	Metrics       MetricsConfig `yaml:"metrics"`
	APIPort       int           `yaml:"api_port"`
	JWTSecret     string        `yaml:"jwt_secret"`
	ZAPIAddress   string        `yaml:"zapi_address"`
	Standalone    bool          `yaml:"standalone"`
}

type Manager struct {
	mu         sync.RWMutex
	config     *Config
	configPath string
}

func NewManager(configPath string) *Manager {
	return &Manager{
		configPath: configPath,
	}
}

// Load loads, validates, and sets the config from the configPath.
func (m *Manager) Load() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	cfg, err := loadAndValidate(m.configPath)
	if err != nil {
		return err
	}

	m.config = cfg
	return nil
}

// Reload reloads the configuration from file. On failure, it rolls back and keeps the old config.
func (m *Manager) Reload() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	cfg, err := loadAndValidate(m.configPath)
	if err != nil {
		return fmt.Errorf("reload validation failed, rolling back: %w", err)
	}

	m.config = cfg
	return nil
}

// Get returns a copy of the current configuration.
func (m *Manager) Get() Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.config == nil {
		return Config{}
	}
	// Return a copy to avoid external mutability issues
	return *m.config
}

func loadAndValidate(path string) (*Config, error) {
	cleanPath := filepath.Clean(path)
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	// Set defaults
	cfg.APIPort = 8080
	cfg.ZAPIAddress = "/var/run/frr/zserv.api"
	cfg.JWTSecret = "olsr-default-jwt-secret-key-must-be-changed"

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse yaml config: %w", err)
	}

	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	return &cfg, nil
}

func validate(cfg *Config) error {
	// RouterID validation
	if cfg.RouterID == "" {
		return fmt.Errorf("router_id is required")
	}
	ip := net.ParseIP(cfg.RouterID)
	if ip == nil || ip.To4() == nil {
		return fmt.Errorf("router_id must be a valid IPv4 address (got: %s)", cfg.RouterID)
	}

	// Interfaces validation
	if len(cfg.Interfaces) == 0 {
		return fmt.Errorf("at least one interface must be specified")
	}
	seen := make(map[string]bool)
	for _, iface := range cfg.Interfaces {
		if iface == "" {
			return fmt.Errorf("interface name cannot be empty")
		}
		if seen[iface] {
			return fmt.Errorf("duplicate interface detected: %s", iface)
		}
		seen[iface] = true
	}

	// Interval validation
	if cfg.HelloInterval <= 0 {
		return fmt.Errorf("hello_interval must be positive (got: %v)", cfg.HelloInterval)
	}
	if cfg.TCInterval <= 0 {
		return fmt.Errorf("tc_interval must be positive (got: %v)", cfg.TCInterval)
	}

	// Metrics validation
	if cfg.Metrics.Type != "etx" && cfg.Metrics.Type != "hopcount" && cfg.Metrics.Type != "" {
		return fmt.Errorf("invalid metrics type: %s (must be 'etx' or 'hopcount')", cfg.Metrics.Type)
	}

	return nil
}

// ValidateFile is a public utility for CLI validation (olsrd-go --check-config)
func ValidateFile(path string) error {
	_, err := loadAndValidate(path)
	return err
}
