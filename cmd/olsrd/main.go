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
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/shjtmy/frr-olsr/internal/config"
	"github.com/shjtmy/frr-olsr/internal/eventbus"
)

const (
	OlsrPort           = 698
	OlsrMulticastGroup = "224.0.0.9"
	PersistenceDir     = "/var/lib/olsrd"
	DefaultConfigPath  = "/etc/olsrd/olsrd.yaml"
)

func main() {
	configPath := flag.String("config", DefaultConfigPath, "Path to the configuration file")
	checkConfig := flag.Bool("check-config", false, "Validate the configuration file and exit")
	standalone := flag.Bool("standalone", false, "Run in standalone mode, configuring routes directly via the Linux kernel instead of FRR/Zebra")
	flag.Parse()

	// Configure structured JSON logging
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	if *checkConfig {
		if err := config.ValidateFile(*configPath); err != nil {
			slog.Error("Configuration validation failed", "error", err)
			os.Exit(1)
		}
		slog.Info("Configuration validation passed")
		os.Exit(0)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := &Daemon{
		cfgMgr:     config.NewManager(*configPath),
		eventBus:   eventbus.NewEventBus(100, 50*time.Millisecond),
		ctx:        ctx,
		cancel:     cancel,
		Standalone: *standalone,
	}

	if err := d.cfgMgr.Load(); err != nil {
		slog.Error("Failed to load initial configuration", "error", err)
		os.Exit(1)
	}

	d.initializeComponents()

	// Handle SIGHUP, SIGINT, SIGTERM
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	go d.handleSignals(sigChan)

	// Run Daemon
	slog.Info("Starting OLSR Go Routing Daemon...")
	d.run()
}
