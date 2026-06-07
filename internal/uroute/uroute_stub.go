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

package uroute

import (
	"log/slog"
)

type StubUnicastRouter struct{}

func newUnicastRouter() UnicastRouter {
	return &StubUnicastRouter{}
}

func (r *StubUnicastRouter) AddRoute(prefix string, nextHop string, ifIndex int, metric int) error {
	slog.Info("[STUB] AddRoute (standalone)", "prefix", prefix, "nextHop", nextHop, "ifIndex", ifIndex, "metric", metric)
	return nil
}

func (r *StubUnicastRouter) DeleteRoute(prefix string, nextHop string, ifIndex int) error {
	slog.Info("[STUB] DeleteRoute (standalone)", "prefix", prefix, "nextHop", nextHop, "ifIndex", ifIndex)
	return nil
}
