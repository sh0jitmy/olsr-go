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

package uroute

import (
	"fmt"
	"net"

	vnetlink "github.com/vishvananda/netlink"
)

type LinuxUnicastRouter struct{}

func newUnicastRouter() UnicastRouter {
	return &LinuxUnicastRouter{}
}

func (r *LinuxUnicastRouter) AddRoute(prefix string, nextHop string, ifIndex int, metric int) error {
	_, ipNet, err := net.ParseCIDR(prefix)
	if err != nil {
		return fmt.Errorf("invalid prefix %s: %w", prefix, err)
	}
	gw := net.ParseIP(nextHop)

	route := &vnetlink.Route{
		LinkIndex: ifIndex,
		Dst:       ipNet,
		Gw:        gw,
		Priority:  metric,
	}
	if err := vnetlink.RouteAdd(route); err != nil {
		return fmt.Errorf("failed to add kernel route %s via %s (dev index %d, metric %d): %w", prefix, nextHop, ifIndex, metric, err)
	}
	return nil
}

func (r *LinuxUnicastRouter) DeleteRoute(prefix string, nextHop string, ifIndex int) error {
	_, ipNet, err := net.ParseCIDR(prefix)
	if err != nil {
		return fmt.Errorf("invalid prefix %s: %w", prefix, err)
	}
	gw := net.ParseIP(nextHop)

	route := &vnetlink.Route{
		LinkIndex: ifIndex,
		Dst:       ipNet,
		Gw:        gw,
	}
	if err := vnetlink.RouteDel(route); err != nil {
		return fmt.Errorf("failed to delete kernel route %s via %s (dev index %d): %w", prefix, nextHop, ifIndex, err)
	}
	return nil
}
