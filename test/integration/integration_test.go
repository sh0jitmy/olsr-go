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
	//nolint:gosec // G101: integration test key is hardcoded by design for test containers
	JWTSecret         = "e2e-secret-key-for-olsrd"
	R1API             = "http://localhost:8081"
	R2API             = "http://localhost:8082"
	R3API             = "http://localhost:8083"
	R4API             = "http://localhost:8084"
	DockerComposeFile = "docker-compose.yml"
)

func createTestToken(subject string) string {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject:   subject,
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
	})
	tokenStr, _ := token.SignedString([]byte(JWTSecret))
	return tokenStr
}

func checkCommandExists(cmd string) bool {
	_, err := exec.LookPath(cmd)
	return err == nil
}

func checkDockerComposeAvailable() (string, bool) {
	// Try "docker compose" first
	cmd := exec.Command("docker", "compose", "version")
	if err := cmd.Run(); err == nil {
		return "docker compose", true
	}
	// Fallback to "docker-compose"
	if checkCommandExists("docker-compose") {
		return "docker-compose", true
	}
	return "", false
}

func TestDockerIntegrationOLSR(t *testing.T) {
	composeCmd, ok := checkDockerComposeAvailable()
	if !ok || !checkCommandExists("docker") {
		t.Skip("Skipping Docker Integration test: docker or docker compose not available")
	}

	token := createTestToken("integration-admin")

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
			fullArgs := append([]string{"compose", "-f", DockerComposeFile}, args...)
			//nolint:gosec // G204: test code launching docker subprocesses
			cmd = exec.Command("docker", fullArgs...)
		} else {
			fullArgs := append([]string{"-f", DockerComposeFile}, args...)
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
	t.Log("Starting docker compose topology...")
	if err := runCompose("up", "-d", "--force-recreate"); err != nil {
		t.Fatalf("failed to start topology: %v", err)
	}
	defer func() {
		t.Log("Stopping and cleaning up docker compose topology...")
		_ = runCompose("down", "-v")
	}()

	// 3. Wait for OLSR Daemon Start & Neighbor Formation
	t.Log("Waiting for neighbors to establish links (convergence)...")
	waitForNeighbors(t, R2API, R4API, token)

	// 4. Test Scenario 1: HNA Route Propagation (Unicast)
	t.Log("Testing Scenario 1: HNA Route Propagation & Removal...")
	integrationTestHNARoutePropagationR1R2(t, token)
	integrationTestHNARoutePropagationR3R4(t, token)

	// 5. Test Scenario 2: Multicast MOLSR MFC Installation (Multicast)
	t.Log("Testing Scenario 2: Multicast MOLSR MFC Installation & Teardown...")
	integrationTestMulticastMFCInstallationR1R2(t, token)
	integrationTestMulticastMFCInstallationR3R4(t, token)
}

func integrationTestHNARoutePropagationR1R2(t *testing.T, token string) {
	// Test Pair 1 (r1 -> r2)
	hna1Payload := map[string]string{"prefix": "192.168.10.0/24"}
	hna1Bytes, _ := json.Marshal(hna1Payload)

	code, _, err := sendRequest(http.MethodPost, R1API+"/api/v1/hna", token, hna1Bytes)
	if err != nil || code != http.StatusCreated {
		t.Fatalf("failed to add HNA prefix to R1: %v (code=%d)", err, code)
	}

	// Check if R2 learns route to 192.168.10.0/24 via Zebra
	learned := false
	for i := 0; i < 15; i++ {
		time.Sleep(1 * time.Second)
		routeCmd := exec.Command("docker", "exec", "olsr-int-r2", "ip", "route", "show", "192.168.10.0/24")
		out, err := routeCmd.CombinedOutput()
		if err == nil && strings.Contains(string(out), "via") {
			learned = true
			t.Logf("R2 successfully learned unicast route: %s", strings.TrimSpace(string(out)))
			break
		}
	}
	if !learned {
		logsCmd := exec.Command("docker", "logs", "olsr-int-r2")
		logsOut, _ := logsCmd.CombinedOutput()
		t.Logf("DIAGNOSTIC: docker logs olsr-int-r2:\n%s", string(logsOut))

		psCmd := exec.Command("docker", "exec", "olsr-int-r2", "ps", "aux")
		psOut, _ := psCmd.CombinedOutput()
		t.Logf("DIAGNOSTIC: processes inside olsr-int-r2:\n%s", string(psOut))

		routesCmd := exec.Command("docker", "exec", "olsr-int-r2", "ip", "route", "show")
		routesOut, _ := routesCmd.CombinedOutput()
		t.Logf("DIAGNOSTIC: kernel routes in olsr-int-r2:\n%s", string(routesOut))

		_, apiOut, _ := sendRequest(http.MethodGet, R2API+"/api/v1/routes", token, nil)
		t.Logf("DIAGNOSTIC: R2 API /api/v1/routes:\n%s", apiOut)

		t.Fatalf("R2 failed to learn unicast HNA route from R1")
	}

	// Remove HNA prefix from R1
	code, _, err = sendRequest(http.MethodDelete, R1API+"/api/v1/hna", token, hna1Bytes)
	if err != nil || code != http.StatusOK {
		t.Fatalf("failed to remove HNA prefix from R1: %v (code=%d)", err, code)
	}

	// Check if R2 removes the route
	removed := false
	for i := 0; i < 15; i++ {
		time.Sleep(1 * time.Second)
		routeCmd := exec.Command("docker", "exec", "olsr-int-r2", "ip", "route", "show", "192.168.10.0/24")
		out, err := routeCmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "via") {
			removed = true
			t.Log("R2 successfully removed unicast route after HNA deletion")
			break
		}
	}
	if !removed {
		t.Fatalf("R2 failed to remove unicast HNA route after R1 deletion")
	}
}

func integrationTestHNARoutePropagationR3R4(t *testing.T, token string) {
	// Test Pair 2 (r3 -> r4)
	hna2Payload := map[string]string{"prefix": "192.168.20.0/24"}
	hna2Bytes, _ := json.Marshal(hna2Payload)

	code, _, err := sendRequest(http.MethodPost, R3API+"/api/v1/hna", token, hna2Bytes)
	if err != nil || code != http.StatusCreated {
		t.Fatalf("failed to add HNA prefix to R3: %v (code=%d)", err, code)
	}

	// Check if R4 learns route to 192.168.20.0/24 via Zebra
	learned := false
	for i := 0; i < 15; i++ {
		time.Sleep(1 * time.Second)
		routeCmd := exec.Command("docker", "exec", "olsr-int-r4", "ip", "route", "show", "192.168.20.0/24")
		out, err := routeCmd.CombinedOutput()
		if err == nil && strings.Contains(string(out), "via") {
			learned = true
			t.Logf("R4 successfully learned unicast route: %s", strings.TrimSpace(string(out)))
			break
		}
	}
	if !learned {
		t.Fatalf("R4 failed to learn unicast HNA route from R3")
	}

	// Remove HNA prefix from R3
	code, _, err = sendRequest(http.MethodDelete, R3API+"/api/v1/hna", token, hna2Bytes)
	if err != nil || code != http.StatusOK {
		t.Fatalf("failed to remove HNA prefix from R3: %v (code=%d)", err, code)
	}

	// Check if R4 removes the route
	removed := false
	for i := 0; i < 15; i++ {
		time.Sleep(1 * time.Second)
		routeCmd := exec.Command("docker", "exec", "olsr-int-r4", "ip", "route", "show", "192.168.20.0/24")
		out, err := routeCmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "via") {
			removed = true
			t.Log("R4 successfully removed unicast route after HNA deletion")
			break
		}
	}
	if !removed {
		t.Fatalf("R4 failed to remove unicast HNA route after R3 deletion")
	}
}

func integrationTestMulticastMFCInstallationR1R2(t *testing.T, token string) {
	// Test Pair 1 (r1 -> r2)
	sc1Body := map[string]interface{}{"source": "10.10.1.10", "group": "239.2.2.2", "duration_seconds": 60}
	sc1Bytes, _ := json.Marshal(sc1Body)
	code, _, err := sendRequest(http.MethodPost, R1API+"/api/v1/molsr/source-claims", token, sc1Bytes)
	if err != nil || code != http.StatusCreated {
		t.Fatalf("failed to inject SourceClaim on R1: %v (code=%d)", err, code)
	}

	cp1Body := map[string]interface{}{
		"source": "10.10.1.10", "group": "239.2.2.2", "parent": "10.10.1.10", "child": "10.10.1.20", "duration_seconds": 60,
	}
	cp1Bytes, _ := json.Marshal(cp1Body)
	code, _, err = sendRequest(http.MethodPost, R2API+"/api/v1/molsr/confirm-parents", token, cp1Bytes)
	if err != nil || code != http.StatusCreated {
		t.Fatalf("failed to register ConfirmParent on R2: %v (code=%d)", err, code)
	}

	// Verify multicast forwarding route (MFC) in R2
	mrouteProgrammed := false
	for i := 0; i < 15; i++ {
		time.Sleep(1 * time.Second)
		mrouteCmd := exec.Command("docker", "exec", "olsr-int-r2", "ip", "mroute", "show")
		out, err := mrouteCmd.CombinedOutput()
		if err == nil && strings.Contains(string(out), "239.2.2.2") {
			mrouteProgrammed = true
			t.Logf("R2 successfully programmed kernel multicast forwarding cache: %s", strings.TrimSpace(string(out)))
			break
		}
	}
	if !mrouteProgrammed {
		t.Fatalf("R2 failed to program kernel multicast forwarding cache")
	}

	// Delete ConfirmParent on R2
	cp1DelBody := map[string]interface{}{"source": "10.10.1.10", "group": "239.2.2.2", "child": "10.10.1.20"}
	cp1DelBytes, _ := json.Marshal(cp1DelBody)
	code, _, err = sendRequest(http.MethodDelete, R2API+"/api/v1/molsr/confirm-parents", token, cp1DelBytes)
	if err != nil || code != http.StatusOK {
		t.Fatalf("failed to delete ConfirmParent on R2: %v (code=%d)", err, code)
	}

	// Verify multicast route is removed from R2
	mrouteRemoved := false
	for i := 0; i < 15; i++ {
		time.Sleep(1 * time.Second)
		mrouteCmd := exec.Command("docker", "exec", "olsr-int-r2", "ip", "mroute", "show")
		out, err := mrouteCmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "239.2.2.2") {
			mrouteRemoved = true
			t.Log("R2 successfully removed kernel multicast route after parent deletion")
			break
		}
	}
	if !mrouteRemoved {
		t.Fatalf("R2 failed to remove kernel multicast route after parent deletion")
	}
}

func integrationTestMulticastMFCInstallationR3R4(t *testing.T, token string) {
	// Test Pair 2 (r3 -> r4)
	sc2Body := map[string]interface{}{"source": "10.10.2.30", "group": "239.3.3.3", "duration_seconds": 60}
	sc2Bytes, _ := json.Marshal(sc2Body)
	code, _, err := sendRequest(http.MethodPost, R3API+"/api/v1/molsr/source-claims", token, sc2Bytes)
	if err != nil || code != http.StatusCreated {
		t.Fatalf("failed to inject SourceClaim on R3: %v (code=%d)", err, code)
	}

	cp2Body := map[string]interface{}{
		"source": "10.10.2.30", "group": "239.3.3.3", "parent": "10.10.2.30", "child": "10.10.2.40", "duration_seconds": 60,
	}
	cp2Bytes, _ := json.Marshal(cp2Body)
	code, _, err = sendRequest(http.MethodPost, R4API+"/api/v1/molsr/confirm-parents", token, cp2Bytes)
	if err != nil || code != http.StatusCreated {
		t.Fatalf("failed to register ConfirmParent on R4: %v (code=%d)", err, code)
	}

	// Verify multicast forwarding route (MFC) in R4
	mrouteProgrammed := false
	for i := 0; i < 15; i++ {
		time.Sleep(1 * time.Second)
		mrouteCmd := exec.Command("docker", "exec", "olsr-int-r4", "ip", "mroute", "show")
		out, err := mrouteCmd.CombinedOutput()
		if err == nil && strings.Contains(string(out), "239.3.3.3") {
			mrouteProgrammed = true
			t.Logf("R4 successfully programmed kernel multicast forwarding cache: %s", strings.TrimSpace(string(out)))
			break
		}
	}
	if !mrouteProgrammed {
		t.Fatalf("R4 failed to program kernel multicast forwarding cache")
	}

	// Delete ConfirmParent on R4
	cp2DelBody := map[string]interface{}{"source": "10.10.2.30", "group": "239.3.3.3", "child": "10.10.2.40"}
	cp2DelBytes, _ := json.Marshal(cp2DelBody)
	code, _, err = sendRequest(http.MethodDelete, R4API+"/api/v1/molsr/confirm-parents", token, cp2DelBytes)
	if err != nil || code != http.StatusOK {
		t.Fatalf("failed to delete ConfirmParent on R4: %v (code=%d)", err, code)
	}

	// Verify multicast route is removed from R4
	mrouteRemoved := false
	for i := 0; i < 15; i++ {
		time.Sleep(1 * time.Second)
		mrouteCmd := exec.Command("docker", "exec", "olsr-int-r4", "ip", "mroute", "show")
		out, err := mrouteCmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "239.3.3.3") {
			mrouteRemoved = true
			t.Log("R4 successfully removed kernel multicast route after parent deletion")
			break
		}
	}
	if !mrouteRemoved {
		t.Fatalf("R4 failed to remove kernel multicast route after parent deletion")
	}
}
