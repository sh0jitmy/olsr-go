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

package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/sh0jitmy/olsr-go/internal/config"
	"github.com/sh0jitmy/olsr-go/internal/eventbus"
	"github.com/sh0jitmy/olsr-go/internal/netlink"
	"github.com/sh0jitmy/olsr-go/internal/olsr"
	"github.com/sh0jitmy/olsr-go/internal/zebra"
)

func createTestJWT(t *testing.T, secret, subject string) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject:   subject,
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	})
	tokenStr, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("failed to sign JWT: %v", err)
	}
	return tokenStr
}

func setupAPIServer(t *testing.T, jwtSecret string) (*APIServer, string) {
	t.Helper()
	bus := eventbus.NewEventBus(10, 10*time.Millisecond)

	// Create mock config file
	configContent := fmt.Sprintf(`
router_id: 1.1.1.1
interfaces:
  - eth0
hello_interval: 2s
tc_interval: 5s
jwt_secret: %s
api_port: 0 # random
`, jwtSecret)

	tmpFile := filepath.Join(t.TempDir(), "olsrd.yaml")
	if err := os.WriteFile(tmpFile, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write mock config: %v", err)
	}

	cfgMgr := config.NewManager(tmpFile)
	if err := cfgMgr.Load(); err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	nm := olsr.NewNeighborManager("1.1.1.1", bus)
	tm := olsr.NewTopologyManager(bus)
	hm := olsr.NewHNAManager(bus)
	mm := olsr.NewMOLSRManager("1.1.1.1", bus)
	lookup := olsr.LocalRouterLookup{RouterID: "1.1.1.1"}
	spf := olsr.NewSPFEngine(lookup, nm, tm, hm, bus)
	zapi := zebra.NewZAPIClient("/tmp/zserv-mock.api", bus)
	mon := netlink.NewMonitor(bus)

	server := NewAPIServer(cfgMgr, nm, tm, hm, mm, spf, zapi, mon)
	// Lower rate limits for testing
	server.limitRate = 2.0
	server.limitBurst = 2

	token := createTestJWT(t, jwtSecret, "test-admin")
	return server, token
}

func TestAPIHealthz(t *testing.T) {
	server, _ := setupAPIServer(t, "my-secret")
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()

	server.handleHealthz(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
	if rr.Body.String() != "OK" {
		t.Errorf("expected Body OK, got %q", rr.Body.String())
	}
}

func TestAPIAuthorizationAndRateLimiting(t *testing.T) {
	secret := "secret-key"
	server, token := setupAPIServer(t, secret)

	// 1. Missing Token (401)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/neighbors", nil)
	rr := httptest.NewRecorder()
	server.jwtAuth(server.handleNeighbors)(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized on missing token, got %d", rr.Code)
	}

	// 2. Invalid Token (401)
	req = httptest.NewRequest(http.MethodGet, "/api/v1/neighbors", nil)
	req.Header.Set("Authorization", "Bearer invalid-token-string")
	rr = httptest.NewRecorder()
	server.jwtAuth(server.handleNeighbors)(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 on bad token, got %d", rr.Code)
	}

	// 3. Valid Token (200)
	req = httptest.NewRequest(http.MethodGet, "/api/v1/neighbors", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr = httptest.NewRecorder()
	server.jwtAuth(server.handleNeighbors)(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 on authorized request, got %d", rr.Code)
	}

	// 4. Rate Limiter Trigger (429)
	// We make multiple requests exceeding rate limits (burst = 2)
	handler := server.rateLimit(server.handleNeighbors)
	for i := 0; i < 5; i++ {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/neighbors", nil)
		req.RemoteAddr = "192.168.1.100:1234"
		rr = httptest.NewRecorder()
		handler(rr, req)
		if i >= 2 && rr.Code == http.StatusTooManyRequests {
			return // Triggered rate limit successfully
		}
	}
	t.Errorf("expected rate limiter to return 429, but all went through")
}

func TestAPIHNAOperations(t *testing.T) {
	server, token := setupAPIServer(t, "my-secret")
	handler := server.jwtAuth(server.handleHNA)

	// 1. GET HNA (empty initially, except maybe local entries if none, currently 0)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/hna", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	handler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET HNA failed: %d", rr.Code)
	}

	// 2. POST HNA
	body := hnaPayload{Prefix: "172.16.0.0/16"}
	bodyBytes, _ := json.Marshal(body)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/hna", bytes.NewBuffer(bodyBytes))
	req.Header.Set("Authorization", "Bearer "+token)
	rr = httptest.NewRecorder()
	handler(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST HNA failed: %d (%s)", rr.Code, rr.Body.String())
	}

	// Check updated listing
	locals := server.hnaMgr.GetLocalHNAs()
	if len(locals) != 1 || locals[0].String() != "172.16.0.0/16" {
		t.Errorf("HNA prefix not registered, got %v", locals)
	}

	// 3. DELETE HNA
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/hna", bytes.NewBuffer(bodyBytes))
	req.Header.Set("Authorization", "Bearer "+token)
	rr = httptest.NewRecorder()
	handler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("DELETE HNA failed: %d", rr.Code)
	}

	// Check listings empty again
	locals = server.hnaMgr.GetLocalHNAs()
	if len(locals) != 0 {
		t.Errorf("HNA prefix not deleted, got %v", locals)
	}
}

func TestAPIMOLSRInjections(t *testing.T) {
	server, token := setupAPIServer(t, "my-secret")

	// 1. Source Claims injection
	scBody := sourceClaimPayload{
		Source:          "10.100.1.1",
		Group:           "239.1.1.1",
		DurationSeconds: 10,
	}
	scBytes, _ := json.Marshal(scBody)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/molsr/source-claims", bytes.NewBuffer(scBytes))
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	server.jwtAuth(server.handleSourceClaims)(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST source claim failed: %d (%s)", rr.Code, rr.Body.String())
	}

	claims := server.molsrMgr.GetSourceClaims()
	if len(claims) != 1 || claims[0].SourceIP != "10.100.1.1" {
		t.Errorf("claim not found in DB")
	}

	// 2. Confirm Parents injection
	cpBody := confirmParentPayload{
		Source:          "10.100.1.1",
		Group:           "239.1.1.1",
		Parent:          "1.1.1.1",
		Child:           "3.3.3.3",
		DurationSeconds: 10,
	}
	cpBytes, _ := json.Marshal(cpBody)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/molsr/confirm-parents", bytes.NewBuffer(cpBytes))
	req.Header.Set("Authorization", "Bearer "+token)
	rr = httptest.NewRecorder()
	server.jwtAuth(server.handleConfirmParents)(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST confirm parent failed: %d (%s)", rr.Code, rr.Body.String())
	}

	parents := server.molsrMgr.GetConfirmParents()
	if len(parents) != 1 || parents[0].ChildIP != "3.3.3.3" {
		t.Errorf("parent entry not found in DB")
	}
}
