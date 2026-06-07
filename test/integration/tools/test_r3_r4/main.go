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
	tokenStr, err := token.SignedString([]byte("e2e-secret-key-for-olsrd"))
	if err != nil {
		fmt.Println("JWT sign error:", err)
		return
	}

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
		defer resp.Body.Close()
		respBytes, _ := io.ReadAll(resp.Body)
		fmt.Printf("[%s] %s -> Status: %d, Body: %s\n", method, url, resp.StatusCode, string(respBytes))
	}

	fmt.Println("--- Injecting HNA into R3 ---")
	sendReq("POST", "http://localhost:8083/api/v1/hna", map[string]string{"prefix": "192.168.20.0/24"})

	fmt.Println("--- Waiting 6 seconds for HNA propagation ---")
	time.Sleep(6 * time.Second)

	fmt.Println("--- Checking kernel route in R4 ---")
	routeCmd := exec.Command("docker", "exec", "olsr-int-r4", "ip", "route", "show", "192.168.20.0/24")
	out, err := routeCmd.CombinedOutput()
	if err == nil {
		fmt.Printf("R4 kernel route: %s\n", strings.TrimSpace(string(out)))
	} else {
		fmt.Printf("R4 kernel route not found or error: %v (output: %s)\n", err, string(out))
	}
}
