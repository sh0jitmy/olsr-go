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
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/shjtmy/frr-olsr/internal/config"
	"github.com/shjtmy/frr-olsr/internal/netlink"
	"github.com/shjtmy/frr-olsr/internal/olsr"
	"github.com/shjtmy/frr-olsr/internal/zebra"
)

type APIServer struct {
	server      *http.Server
	configMgr   *config.Manager
	neighMgr    *olsr.NeighborManager
	topoMgr     *olsr.TopologyManager
	hnaMgr      *olsr.HNAManager
	molsrMgr    *olsr.MOLSRManager
	spfEngine   *olsr.SPFEngine
	zapiClient  *zebra.ZAPIClient
	monitor     netlink.Monitor
	
	// Rate Limiting parameters
	mu          sync.Mutex
	rateLimits  map[string]*clientBucket
	limitRate   float64
	limitBurst  int
}

type clientBucket struct {
	tokens     float64
	lastUpdate time.Time
}

func NewAPIServer(
	cfgMgr *config.Manager,
	nm *olsr.NeighborManager,
	tm *olsr.TopologyManager,
	hm *olsr.HNAManager,
	mm *olsr.MOLSRManager,
	spf *olsr.SPFEngine,
	zapi *zebra.ZAPIClient,
	mon netlink.Monitor,
) *APIServer {
	return &APIServer{
		configMgr:  cfgMgr,
		neighMgr:   nm,
		topoMgr:    tm,
		hnaMgr:     hm,
		molsrMgr:   mm,
		spfEngine:  spf,
		zapiClient: zapi,
		monitor:    mon,
		rateLimits: make(map[string]*clientBucket),
		limitRate:  10.0, // 10 requests per second
		limitBurst: 20,
	}
}

func (s *APIServer) Start(ctx context.Context) error {
	cfg := s.configMgr.Get()
	mux := http.NewServeMux()

	// Public Health API
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/readyz", s.handleReadyz)
	mux.Handle("/metrics", promhttp.Handler())

	// Secured API under v1
	mux.HandleFunc("/api/v1/neighbors", s.auditLog(s.rateLimit(s.jwtAuth(s.handleNeighbors))))
	mux.HandleFunc("/api/v1/topology", s.auditLog(s.rateLimit(s.jwtAuth(s.handleTopology))))
	mux.HandleFunc("/api/v1/routes", s.auditLog(s.rateLimit(s.jwtAuth(s.handleRoutes))))
	mux.HandleFunc("/api/v1/interfaces", s.auditLog(s.rateLimit(s.jwtAuth(s.handleInterfaces))))
	mux.HandleFunc("/api/v1/hna", s.auditLog(s.rateLimit(s.jwtAuth(s.handleHNA))))
	mux.HandleFunc("/api/v1/molsr/source-claims", s.auditLog(s.rateLimit(s.jwtAuth(s.handleSourceClaims))))
	mux.HandleFunc("/api/v1/molsr/confirm-parents", s.auditLog(s.rateLimit(s.jwtAuth(s.handleConfirmParents))))

	addr := fmt.Sprintf(":%d", cfg.APIPort)
	s.server = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	slog.Info("Starting API server", "addr", addr)
	// For production, if cert files are configured we would use ListenAndServeTLS.
	// For simplicity, we fallback to ListenAndServe if no certificates exist.
	go func() {
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("API server listen failure", "error", err)
		}
	}()

	return nil
}

func (s *APIServer) Stop(ctx context.Context) error {
	if s.server != nil {
		return s.server.Shutdown(ctx)
	}
	return nil
}

// Security Middlewares

func (s *APIServer) auditLog(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		user := r.Header.Get("X-Audit-User")
		if user == "" {
			user = "anonymous"
		}
		
		clientIP, _, _ := net.SplitHostPort(r.RemoteAddr)
		if clientIP == "" {
			clientIP = r.RemoteAddr
		}

		next(w, r)

		duration := time.Since(start)
		// Audit structured log output
		slog.Info("Audit Log",
			"timestamp", time.Now().Format(time.RFC3339),
			"level", "AUDIT",
			"client_ip", clientIP,
			"user", user,
			"method", r.Method,
			"path", r.URL.Path,
			"duration_ms", duration.Milliseconds(),
		)
	}
}

func (s *APIServer) rateLimit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		clientIP, _, _ := net.SplitHostPort(r.RemoteAddr)
		if clientIP == "" {
			clientIP = r.RemoteAddr
		}

		s.mu.Lock()
		bucket, exists := s.rateLimits[clientIP]
		if !exists {
			bucket = &clientBucket{
				tokens:     float64(s.limitBurst),
				lastUpdate: time.Now(),
			}
			s.rateLimits[clientIP] = bucket
		}

		now := time.Now()
		elapsed := now.Sub(bucket.lastUpdate).Seconds()
		bucket.lastUpdate = now

		// Refill tokens
		bucket.tokens += elapsed * s.limitRate
		if bucket.tokens > float64(s.limitBurst) {
			bucket.tokens = float64(s.limitBurst)
		}

		if bucket.tokens < 1.0 {
			s.mu.Unlock()
			http.Error(w, `{"error": "Too Many Requests"}`, http.StatusTooManyRequests)
			return
		}

		bucket.tokens -= 1.0
		s.mu.Unlock()

		next(w, r)
	}
}

func (s *APIServer) jwtAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Verify JWT Header
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, `{"error": "Missing authorization token"}`, http.StatusUnauthorized)
			return
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
			http.Error(w, `{"error": "Invalid authorization header format"}`, http.StatusUnauthorized)
			return
		}

		tokenStr := parts[1]
		cfg := s.configMgr.Get()

		token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}
			return []byte(cfg.JWTSecret), nil
		})

		if err != nil || !token.Valid {
			http.Error(w, `{"error": "Invalid or expired token"}`, http.StatusUnauthorized)
			return
		}

		// Extract subject as user context for auditing
		if claims, ok := token.Claims.(jwt.MapClaims); ok {
			if sub, err := claims.GetSubject(); err == nil && sub != "" {
				r.Header.Set("X-Audit-User", sub)
			}
		}

		next(w, r)
	}
}

// Utility response helpers
func jsonResponse(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
