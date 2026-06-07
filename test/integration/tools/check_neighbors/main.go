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
	"fmt"
	"io"
	"net/http"
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

	query := func(url string) {
		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("Authorization", "Bearer "+tokenStr)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			fmt.Printf("HTTP client error for %s: %v\n", url, err)
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("%s -> Status: %d, Body:\n%s\n\n", url, resp.StatusCode, string(body))
	}

	fmt.Println("=== Checking neighbors on all 4 routing containers ===")
	query("http://localhost:8081/api/v1/neighbors")
	query("http://localhost:8082/api/v1/neighbors")
	query("http://localhost:8083/api/v1/neighbors")
	query("http://localhost:8084/api/v1/neighbors")
}
