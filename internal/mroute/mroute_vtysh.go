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
	"fmt"
	"log/slog"
	"net"
	"os/exec"
)

type VTYSHMulticastRouter struct {
	interfaces  []string
	execCommand func(name string, arg ...string) *exec.Cmd
}

func NewVTYSHMulticastRouter(interfaces []string) MulticastRouter {
	return &VTYSHMulticastRouter{
		interfaces:  interfaces,
		execCommand: exec.Command,
	}
}

func (r *VTYSHMulticastRouter) Start() error {
	slog.Info("VTYSH multicast router started")
	for _, iface := range r.interfaces {
		slog.Info("Enabling PIM on interface via vtysh", "interface", iface)
		cmd := r.execCommand("vtysh", "-c", "configure terminal", "-c", "interface "+iface, "-c", "ip pim")
		out, err := cmd.CombinedOutput()
		if err != nil {
			slog.Error("Failed to enable PIM on interface", "interface", iface, "error", err, "output", string(out))
		}
	}
	return nil
}

func (r *VTYSHMulticastRouter) Stop() error {
	slog.Info("VTYSH multicast router stopped")
	return nil
}

func (r *VTYSHMulticastRouter) AddMulticastRoute(srcIP, grpIP string, iif int, oifs []int) error {
	iifName, err := resolveInterfaceName(iif)
	if err != nil {
		return fmt.Errorf("failed to resolve IIF name: %w", err)
	}

	oifNames := make([]string, 0)
	if len(oifs) == 0 {
		oifNames = append(oifNames, "lo")
	} else {
		for _, oif := range oifs {
			oifName, err := resolveInterfaceName(oif)
			if err != nil {
				slog.Warn("Failed to resolve OIF name, skipping", "index", oif, "error", err)
				continue
			}
			oifNames = append(oifNames, oifName)
		}
	}

	for _, oifName := range oifNames {
		// Enable PIM on the incoming interface first (required by FRR)
		cmdPimIif := r.execCommand("vtysh", "-c", "configure terminal", "-c", "interface "+iifName, "-c", "ip pim")
		if out, err := cmdPimIif.CombinedOutput(); err != nil {
			slog.Warn("Failed to enable PIM on incoming interface", "interface", iifName, "error", err, "output", string(out))
		}

		// Enable PIM on the outgoing interface first (required by FRR)
		if oifName != iifName {
			cmdPimOif := r.execCommand("vtysh", "-c", "configure terminal", "-c", "interface "+oifName, "-c", "ip pim")
			if out, err := cmdPimOif.CombinedOutput(); err != nil {
				slog.Warn("Failed to enable PIM on outgoing interface", "interface", oifName, "error", err, "output", string(out))
			}
		}

		slog.Info("Adding static multicast route via vtysh", "src", srcIP, "grp", grpIP, "iif", iifName, "oif", oifName)
		cmd := r.execCommand("vtysh", "-c", "configure terminal", "-c", "interface "+iifName, "-c", fmt.Sprintf("ip mroute %s %s %s", oifName, grpIP, srcIP))
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("failed to run vtysh to add mroute: %w (output: %s)", err, string(out))
		}
	}
	return nil
}

func (r *VTYSHMulticastRouter) DeleteMulticastRoute(srcIP, grpIP string, iif int, oifs []int) error {
	iifName, err := resolveInterfaceName(iif)
	if err != nil {
		return fmt.Errorf("failed to resolve IIF name: %w", err)
	}

	oifNames := make([]string, 0)
	if len(oifs) == 0 {
		oifNames = append(oifNames, "lo")
	} else {
		for _, oif := range oifs {
			oifName, err := resolveInterfaceName(oif)
			if err != nil {
				slog.Warn("Failed to resolve OIF name, skipping", "index", oif, "error", err)
				continue
			}
			oifNames = append(oifNames, oifName)
		}
	}

	for _, oifName := range oifNames {
		slog.Info("Deleting static multicast route via vtysh", "src", srcIP, "grp", grpIP, "iif", iifName, "oif", oifName)
		cmd := r.execCommand("vtysh", "-c", "configure terminal", "-c", "interface "+iifName, "-c", fmt.Sprintf("no ip mroute %s %s %s", oifName, grpIP, srcIP))
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("failed to run vtysh to delete mroute: %w (output: %s)", err, string(out))
		}
	}
	return nil
}

func resolveInterfaceName(index int) (string, error) {
	iface, err := net.InterfaceByIndex(index)
	if err != nil {
		return "", err
	}
	return iface.Name, nil
}
