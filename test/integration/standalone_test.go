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

package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	//nolint:gosec // G101: standalone test key is hardcoded by design for test containers
	StandaloneJWTSecret   = "e2e-secret-key-for-olsrd"
	StandR1API            = "http://localhost:8091"
	StandR2API            = "http://localhost:8092"
	StandR3API            = "http://localhost:8093"
	StandR4API            = "http://localhost:8094"
	StandaloneComposeFile = "docker-compose-standalone.yml"
)

func createStandaloneTestToken(subject string) string {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject:   subject,
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
	})
	tokenStr, _ := token.SignedString([]byte(StandaloneJWTSecret))
	return tokenStr
}

func TestDockerStandaloneOLSR(t *testing.T) {
	composeCmd, ok := checkDockerComposeAvailable()
	if !ok || !checkCommandExists("docker") {
		t.Skip("Skipping Docker Standalone Integration test: docker or docker compose not available")
	}

	token := createStandaloneTestToken("integration-admin")

	// 1. Build local binary first
	t.Log("Building olsrd binary...")
	buildCmd := exec.Command("go", "build", "-o", "../../bin/olsrd", "../../cmd/olsrd/")
	buildCmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+runtime.GOARCH)
	if err := buildCmd.Run(); err != nil {
		t.Fatalf("failed to build binary: %v", err)
	}

	// Helper function to run docker compose
	runCompose := func(args ...string) error {
		var cmd *exec.Cmd
		if composeCmd == "docker compose" {
			fullArgs := append([]string{"compose", "-f", StandaloneComposeFile}, args...)
			//nolint:gosec // G204: test code launching docker subprocesses
			cmd = exec.Command("docker", fullArgs...)
		} else {
			fullArgs := append([]string{"-f", StandaloneComposeFile}, args...)
			//nolint:gosec // G204: test code launching docker-compose subprocesses
			cmd = exec.Command("docker-compose", fullArgs...)
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%v: %s", err, string(out))
		}
		return nil
	}

	// 2. Deploy topology
	t.Log("Starting docker compose standalone topology...")
	if err := runCompose("up", "-d", "--force-recreate"); err != nil {
		t.Fatalf("failed to start topology: %v", err)
	}
	defer func() {
		t.Log("Stopping and cleaning up docker compose standalone topology...")
		_ = runCompose("down", "-v")
	}()

	// 3. Wait for OLSR Daemon Start & Neighbor Formation
	t.Log("Waiting for neighbors to establish links (convergence)...")
	waitForNeighbors(t, StandR2API, StandR4API, token)

	// 4. Test Scenario 1: HNA Route Propagation (Unicast)
	t.Log("Testing Scenario 1: HNA Route Propagation & Removal...")
	standaloneTestHNARoutePropagationR1R2(t, token)
	standaloneTestHNARoutePropagationR3R4(t, token)

	// 5. Test Scenario 2: Multicast MOLSR MFC Installation (Multicast)
	t.Log("Testing Scenario 2: Multicast MOLSR MFC Installation & Teardown...")
	standaloneTestMulticastMFCInstallationR1R2(t, token)
}

func standaloneTestHNARoutePropagationR1R2(t *testing.T, token string) {
	// Test Pair 1 (r1 -> r2)
	hna1Payload := map[string]string{"prefix": "192.168.10.0/24"}
	hna1Bytes, _ := json.Marshal(hna1Payload)

	code, _, err := sendRequest(http.MethodPost, StandR1API+"/api/v1/hna", token, hna1Bytes)
	if err != nil || code != http.StatusCreated {
		t.Fatalf("failed to add HNA prefix to R1: %v (code=%d)", err, code)
	}

	// Check if R2 learns route to 192.168.10.0/24 directly in the kernel
	learned := false
	for i := 0; i < 15; i++ {
		time.Sleep(1 * time.Second)
		routeCmd := exec.Command("docker", "exec", "olsr-stand-r2", "ip", "route", "show", "192.168.10.0/24")
		out, err := routeCmd.CombinedOutput()
		if err == nil && strings.Contains(string(out), "via") {
			learned = true
			t.Logf("R2 successfully learned standalone unicast route: %s", strings.TrimSpace(string(out)))
			break
		}
	}
	if !learned {
		logsCmd := exec.Command("docker", "logs", "olsr-stand-r2")
		logsOut, _ := logsCmd.CombinedOutput()
		t.Logf("DIAGNOSTIC: docker logs olsr-stand-r2:\n%s", string(logsOut))

		routesCmd := exec.Command("docker", "exec", "olsr-stand-r2", "ip", "route", "show")
		routesOut, _ := routesCmd.CombinedOutput()
		t.Logf("DIAGNOSTIC: kernel routes in olsr-stand-r2:\n%s", string(routesOut))

		t.Fatalf("R2 failed to learn standalone unicast HNA route from R1")
	}

	// Remove HNA prefix from R1
	code, _, err = sendRequest(http.MethodDelete, StandR1API+"/api/v1/hna", token, hna1Bytes)
	if err != nil || code != http.StatusOK {
		t.Fatalf("failed to remove HNA prefix from R1: %v (code=%d)", err, code)
	}

	// Check if R2 removes the route
	removed := false
	for i := 0; i < 15; i++ {
		time.Sleep(1 * time.Second)
		routeCmd := exec.Command("docker", "exec", "olsr-stand-r2", "ip", "route", "show", "192.168.10.0/24")
		out, err := routeCmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "via") {
			removed = true
			t.Log("R2 successfully removed standalone unicast route after HNA deletion")
			break
		}
	}
	if !removed {
		t.Fatalf("R2 failed to remove standalone unicast HNA route after R1 deletion")
	}
}

func standaloneTestHNARoutePropagationR3R4(t *testing.T, token string) {
	// Test Pair 2 (r3 -> r4)
	hna2Payload := map[string]string{"prefix": "192.168.20.0/24"}
	hna2Bytes, _ := json.Marshal(hna2Payload)

	code, _, err := sendRequest(http.MethodPost, StandR3API+"/api/v1/hna", token, hna2Bytes)
	if err != nil || code != http.StatusCreated {
		t.Fatalf("failed to add HNA prefix to R3: %v (code=%d)", err, code)
	}

	// Check if R4 learns route to 192.168.20.0/24
	learned := false
	for i := 0; i < 15; i++ {
		time.Sleep(1 * time.Second)
		routeCmd := exec.Command("docker", "exec", "olsr-stand-r4", "ip", "route", "show", "192.168.20.0/24")
		out, err := routeCmd.CombinedOutput()
		if err == nil && strings.Contains(string(out), "via") {
			learned = true
			t.Logf("R4 successfully learned standalone unicast route: %s", strings.TrimSpace(string(out)))
			break
		}
	}
	if !learned {
		t.Fatalf("R4 failed to learn standalone unicast HNA route from R3")
	}

	// Remove HNA prefix from R3
	code, _, err = sendRequest(http.MethodDelete, StandR3API+"/api/v1/hna", token, hna2Bytes)
	if err != nil || code != http.StatusOK {
		t.Fatalf("failed to remove HNA prefix from R3: %v (code=%d)", err, code)
	}

	// Check if R4 removes the route
	removed := false
	for i := 0; i < 15; i++ {
		time.Sleep(1 * time.Second)
		routeCmd := exec.Command("docker", "exec", "olsr-stand-r4", "ip", "route", "show", "192.168.20.0/24")
		out, err := routeCmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "via") {
			removed = true
			t.Log("R4 successfully removed standalone unicast route after HNA deletion")
			break
		}
	}
	if !removed {
		t.Fatalf("R4 failed to remove standalone unicast HNA route after R3 deletion")
	}
}

func standaloneTestMulticastMFCInstallationR1R2(t *testing.T, token string) {
	// Test Pair 1 (r1 -> r2)
	sc1Body := map[string]interface{}{"source": "10.10.1.10", "group": "239.2.2.2", "duration_seconds": 60}
	sc1Bytes, _ := json.Marshal(sc1Body)
	code, _, err := sendRequest(http.MethodPost, StandR1API+"/api/v1/molsr/source-claims", token, sc1Bytes)
	if err != nil || code != http.StatusCreated {
		t.Fatalf("failed to inject SourceClaim on R1: %v (code=%d)", err, code)
	}

	cp1Body := map[string]interface{}{
		"source": "10.10.1.10", "group": "239.2.2.2", "parent": "10.10.1.10", "child": "10.10.1.20", "duration_seconds": 60,
	}
	cp1Bytes, _ := json.Marshal(cp1Body)
	code, _, err = sendRequest(http.MethodPost, StandR2API+"/api/v1/molsr/confirm-parents", token, cp1Bytes)
	if err != nil || code != http.StatusCreated {
		t.Fatalf("failed to register ConfirmParent on R2: %v (code=%d)", err, code)
	}

	// Verify multicast forwarding route (MFC) in R2
	mrouteProgrammed := false
	for i := 0; i < 15; i++ {
		time.Sleep(1 * time.Second)
		mrouteCmd := exec.Command("docker", "exec", "olsr-stand-r2", "ip", "mroute", "show")
		out, err := mrouteCmd.CombinedOutput()
		if err == nil && strings.Contains(string(out), "239.2.2.2") {
			mrouteProgrammed = true
			t.Logf("R2 successfully programmed standalone kernel multicast forwarding cache: %s", strings.TrimSpace(string(out)))
			break
		}
	}
	if !mrouteProgrammed {
		t.Fatalf("R2 failed to program standalone kernel multicast forwarding cache")
	}

	// Delete ConfirmParent on R2
	cp1DelBody := map[string]interface{}{"source": "10.10.1.10", "group": "239.2.2.2", "child": "10.10.1.20"}
	cp1DelBytes, _ := json.Marshal(cp1DelBody)
	code, _, err = sendRequest(http.MethodDelete, StandR2API+"/api/v1/molsr/confirm-parents", token, cp1DelBytes)
	if err != nil || code != http.StatusOK {
		t.Fatalf("failed to delete ConfirmParent on R2: %v (code=%d)", err, code)
	}

	// Verify multicast route is removed from R2
	mrouteRemoved := false
	for i := 0; i < 15; i++ {
		time.Sleep(1 * time.Second)
		mrouteCmd := exec.Command("docker", "exec", "olsr-stand-r2", "ip", "mroute", "show")
		out, err := mrouteCmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "239.2.2.2") {
			mrouteRemoved = true
			t.Log("R2 successfully removed standalone kernel multicast route after parent deletion")
			break
		}
	}
	if !mrouteRemoved {
		t.Fatalf("R2 failed to remove standalone kernel multicast route after parent deletion")
	}
}
