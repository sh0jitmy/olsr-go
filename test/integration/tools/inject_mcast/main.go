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

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func main() {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject:   "integration-admin",
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
	})
	tokenStr, _ := token.SignedString([]byte("e2e-secret-key-for-olsrd"))

	sendReq := func(method, url string, body interface{}) {
		var reqBody io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			reqBody = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, url, reqBody)
		req.Header.Set("Authorization", "Bearer "+tokenStr)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			fmt.Printf("Error requesting %s: %v\n", url, err)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		respBytes, _ := io.ReadAll(resp.Body)
		fmt.Printf("[%s] %s -> Status: %d, Body: %s\n", method, url, resp.StatusCode, string(respBytes))
	}

	fmt.Println("=== Injecting Multicast SourceClaim into R1 ===")
	sendReq("POST", "http://localhost:8081/api/v1/molsr/source-claims", map[string]interface{}{
		"source":           "10.10.1.10",
		"group":            "239.2.2.2",
		"duration_seconds": 60,
	})

	fmt.Println("=== Registering Multicast ConfirmParent on R2 ===")
	sendReq("POST", "http://localhost:8082/api/v1/molsr/confirm-parents", map[string]interface{}{
		"source":           "10.10.1.10",
		"group":            "239.2.2.2",
		"parent":           "10.10.1.10",
		"child":            "10.10.1.20",
		"duration_seconds": 60,
	})

	fmt.Println("=== Waiting 2 seconds for multicast route installation ===")
	time.Sleep(2 * time.Second)

	fmt.Println("=== Querying R2 active routes ===")
	sendReq("GET", "http://localhost:8082/api/v1/routes", nil)

	fmt.Println("=== Checking kernel multicast route in R2 ===")
	mrouteCmd := exec.Command("docker", "exec", "olsr-int-r2", "ip", "mroute", "show")
	out, err := mrouteCmd.CombinedOutput()
	if err == nil {
		fmt.Printf("R2 kernel multicast routes:\n%s\n", strings.TrimSpace(string(out)))
	} else {
		fmt.Printf("R2 kernel multicast route check failed: %v (output: %s)\n", err, string(out))
	}
}
