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

//go:build !linux

package mroute

import (
	"context"
	"log/slog"
)

type StubLoopPreventionManager struct {
	mode string
}

func NewNFTablesManager() LoopPreventionManager {
	return &StubLoopPreventionManager{mode: "nftables"}
}

func NewNFQueueManager() LoopPreventionManager {
	return &StubLoopPreventionManager{mode: "nfqueue"}
}

func (m *StubLoopPreventionManager) Start(ctx context.Context, interfaces []string) error {
	slog.Warn("Multicast loop prevention is not supported on this platform", "platform", "non-linux", "mode", m.mode)
	return nil
}

func (m *StubLoopPreventionManager) Stop() error {
	return nil
}

func (m *StubLoopPreventionManager) OnMulticastRouteAdd(srcIP, grpIP string, nextHopIP string) error {
	return nil
}

func (m *StubLoopPreventionManager) OnMulticastRouteDelete(srcIP, grpIP string) error {
	return nil
}
