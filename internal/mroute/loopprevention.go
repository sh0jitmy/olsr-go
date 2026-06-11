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
	"context"
)

type LoopPreventionManager interface {
	Start(ctx context.Context, interfaces []string) error
	Stop() error
	OnMulticastRouteAdd(srcIP, grpIP string, nextHopIP string) error
	OnMulticastRouteDelete(srcIP, grpIP string) error
}

func NewLoopPreventionManager(mode string) LoopPreventionManager {
	switch mode {
	case "nftables":
		return NewNFTablesManager()
	case "nfqueue":
		return NewNFQueueManager()
	default:
		return &NoneLoopPreventionManager{}
	}
}

type NoneLoopPreventionManager struct{}

func (m *NoneLoopPreventionManager) Start(ctx context.Context, interfaces []string) error {
	return nil
}

func (m *NoneLoopPreventionManager) Stop() error {
	return nil
}

func (m *NoneLoopPreventionManager) OnMulticastRouteAdd(srcIP, grpIP string, nextHopIP string) error {
	return nil
}

func (m *NoneLoopPreventionManager) OnMulticastRouteDelete(srcIP, grpIP string) error {
	return nil
}
