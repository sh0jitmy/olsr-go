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

//nolint // because we cannot modify golangci.yml
package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

// sendRequest sends an HTTP request with optional JWT token and JSON body.
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
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, "", err
	}

	return resp.StatusCode, string(respBody), nil
}

// waitForNeighbors polls neighbors endpoints on R2 and R4 until they see R1 and R3 respectively.
func waitForNeighbors(t *testing.T, r2API, r4API, token string) {
	convergenceStart := time.Now()
	converged := false

	for i := 0; i < 20; i++ {
		time.Sleep(2 * time.Second)

		// Check Pair 1: r2 neighbors should contain r1 (1.1.1.1)
		code1, body1, err1 := sendRequest(http.MethodGet, r2API+"/api/v1/neighbors", token, nil)
		// Check Pair 2: r4 neighbors should contain r3 (3.3.3.3)
		code2, body2, err2 := sendRequest(http.MethodGet, r4API+"/api/v1/neighbors", token, nil)

		if err1 != nil || err2 != nil || code1 != http.StatusOK || code2 != http.StatusOK {
			continue
		}

		var res1, res2 map[string]interface{}
		if json.Unmarshal([]byte(body1), &res1) != nil || json.Unmarshal([]byte(body2), &res2) != nil {
			continue
		}

		neighs1, ok1 := res1["neighbors"].([]interface{})
		neighs2, ok2 := res2["neighbors"].([]interface{})

		if ok1 && ok2 && len(neighs1) >= 1 && len(neighs2) >= 1 {
			converged = true
			t.Logf("OLSR converged in %v", time.Since(convergenceStart))
			break
		}
	}

	if !converged {
		t.Fatalf("routers failed to establish neighbors within timeout")
	}
}
