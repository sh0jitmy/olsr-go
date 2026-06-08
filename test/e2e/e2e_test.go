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

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	//nolint:gosec // G101: e2e test JWT secret key is hardcoded by design for test containers
	JWTSecret       = "e2e-secret-key-for-olsrd"
	R1API           = "http://localhost:8081"
	R2API           = "http://localhost:8082"
	R3API           = "http://localhost:8083"
	ClabTopology    = "clab/topology.yaml"
)

func createTestToken(subject string) string {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject:   subject,
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
	})
	tokenStr, _ := token.SignedString([]byte(JWTSecret))
	return tokenStr
}

func sendRequest(method, url, token string, body []byte) (int, string, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return 0, "", err
	}

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, "", err
	}

	return resp.StatusCode, string(respBody), nil
}

func checkCommandExists(cmd string) bool {
	_, err := exec.LookPath(cmd)
	return err == nil
}

func TestE2EContainerlabOLSR(t *testing.T) {
	// 1. Skip test if containerlab or docker is not available (ensures macOS compatibility)
	if !checkCommandExists("containerlab") || !checkCommandExists("docker") {
		t.Skip("Skipping E2E test: containerlab or docker not installed")
	}

	token := createTestToken("e2e-admin")

	// 2. Build local binary first
	t.Log("Building olsrd binary...")
	buildCmd := exec.Command("go", "build", "-o", "../../bin/olsrd", "../../cmd/olsrd/")
	if err := buildCmd.Run(); err != nil {
		t.Fatalf("failed to build binary: %v", err)
	}

	// 3. Deploy containerlab topology
	t.Log("Deploying containerlab topology...")
	deployCmd := exec.Command("sudo", "containerlab", "deploy", "-t", ClabTopology, "--reconfigure")
	if out, err := deployCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to deploy containerlab: %v\nOutput: %s", err, string(out))
	}
	defer func() {
		t.Log("Destroying containerlab topology...")
		destroyCmd := exec.Command("sudo", "containerlab", "destroy", "-t", ClabTopology, "--cleanup")
		_ = destroyCmd.Run()
	}()

	// 4. Wait for OLSR Daemon Start & Neighbor Formation
	t.Log("Waiting for routers to establish links (convergence)...")
	convergenceStart := time.Now()
	converged := false

	for i := 0; i < 15; i++ {
		time.Sleep(2 * time.Second)

		// Fetch R2 neighbors (R2 is connected to both R1 and R3)
		code, body, err := sendRequest(http.MethodGet, R2API+"/api/v1/neighbors", token, nil)
		if err != nil || code != http.StatusOK {
			continue
		}

		// Parse neighbors JSON
		var result map[string]interface{}
		if err := json.Unmarshal([]byte(body), &result); err != nil {
			continue
		}

		neighbors, ok := result["neighbors"].([]interface{})
		if ok && len(neighbors) >= 2 {
			converged = true
			t.Logf("OLSR converged in %v", time.Since(convergenceStart))
			break
		}
	}

	if !converged {
		t.Fatalf("routers failed to establish neighbors within timeout")
	}

	// 5. Test HNA Route Propagation (Unicast routing)
	t.Log("Testing HNA route propagation...")
	hnaPayload := map[string]string{"prefix": "172.16.100.0/24"}
	payloadBytes, _ := json.Marshal(hnaPayload)

	// Post HNA to R1 (R1 will advertise 172.16.100.0/24 to mesh)
	code, _, err := sendRequest(http.MethodPost, R1API+"/api/v1/hna", token, payloadBytes)
	if err != nil || code != http.StatusCreated {
		t.Fatalf("failed to add HNA prefix to R1: %v (code=%d)", err, code)
	}

	// Check if R3 learns the route to 172.16.100.0/24 via Zebra
	learned := false
	for i := 0; i < 10; i++ {
		time.Sleep(1 * time.Second)

		// Exec 'ip route' inside R3 container
		routeCmd := exec.Command("docker", "exec", "clab-olsr-lab-r3", "ip", "route", "show", "172.16.100.0/24")
		out, err := routeCmd.CombinedOutput()
		if err == nil && strings.Contains(string(out), "via") {
			learned = true
			t.Logf("R3 learned unicast route successfully: %s", strings.TrimSpace(string(out)))
			break
		}
	}

	if !learned {
		t.Fatalf("R3 failed to learn unicast HNA route")
	}

	// 6. Test MOLSR Multicast Route Installation
	t.Log("Testing MOLSR multicast route installation...")

	// R1 acts as Source: inject SourceClaim for flow 10.0.1.1 (R1 IP) -> 239.1.1.1
	scBody := map[string]interface{}{"source": "10.0.1.1", "group": "239.1.1.1", "duration_seconds": 60}
	scBytes, _ := json.Marshal(scBody)
	_, _, _ = sendRequest(http.MethodPost, R1API+"/api/v1/molsr/source-claims", token, scBytes)

	// R3 joins: designates R2 as Parent
	cpBody := map[string]interface{}{
		"source": "10.0.1.1", "group": "239.1.1.1", "parent": "2.2.2.2", "child": "3.3.3.3", "duration_seconds": 60,
	}
	cpBytes, _ := json.Marshal(cpBody)
	_, _, _ = sendRequest(http.MethodPost, R2API+"/api/v1/molsr/confirm-parents", token, cpBytes)

	// Check if R2 programs the kernel multicast route (MFC)
	mcastProgrammed := false
	for i := 0; i < 10; i++ {
		time.Sleep(1 * time.Second)

		mrouteCmd := exec.Command("docker", "exec", "clab-olsr-lab-r2", "ip", "mroute", "show")
		out, err := mrouteCmd.CombinedOutput()
		if err == nil && strings.Contains(string(out), "239.1.1.1") {
			mcastProgrammed = true
			t.Logf("R2 successfully programmed kernel multicast forwarding cache: %s", strings.TrimSpace(string(out)))
			break
		}
	}

	if !mcastProgrammed {
		t.Fatalf("R2 failed to program kernel multicast forwarding cache")
	}

	// 7. Test Zebra Disconnect Recovery
	t.Log("Testing Zebra disconnect recovery...")
	// Kill zebra on R3
	killCmd := exec.Command("docker", "exec", "clab-olsr-lab-r3", "killall", "zebra")
	_ = killCmd.Run()

	// Wait 2 seconds
	time.Sleep(2 * time.Second)

	// Start zebra back up on R3
	startZebraCmd := exec.Command("docker", "exec", "clab-olsr-lab-r3", "/usr/lib/frr/zebra", "-d")
	if err := startZebraCmd.Run(); err != nil {
		t.Fatalf("failed to restart Zebra on R3: %v", err)
	}

	// Check if routes are re-synced in R3's kernel
	recovered := false
	for i := 0; i < 10; i++ {
		time.Sleep(1 * time.Second)

		routeCmd := exec.Command("docker", "exec", "clab-olsr-lab-r3", "ip", "route", "show", "172.16.100.0/24")
		out, err := routeCmd.CombinedOutput()
		if err == nil && strings.Contains(string(out), "via") {
			recovered = true
			t.Log("R3 successfully recovered and re-synced routes with Zebra")
			break
		}
	}

	if !recovered {
		t.Fatalf("Zebra recovery and route synchronization failed")
	}
}
