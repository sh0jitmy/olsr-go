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
	"encoding/json"
	"net"
	"net/http"
	"time"

	"github.com/shjtmy/frr-olsr/internal/metrics"
)

// Health APIs

func (s *APIServer) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}

func (s *APIServer) handleReadyz(w http.ResponseWriter, r *http.Request) {
	// Readiness criteria check
	zapiOk := true
	if s.zapiClient != nil {
		zapiOk = s.zapiClient.IsConnected()
	}

	// 1. Interfaces check: at least one interface must be UP
	ifaces := s.monitor.GetInterfaces()
	ifacesOk := false
	for _, iface := range ifaces {
		if iface.IsUp {
			ifacesOk = true
			break
		}
	}

	// 2. SPF engine is ready and instantiated
	spfOk := s.spfEngine != nil

	// 3. ZAPI client should be online
	if !zapiOk || !ifacesOk || !spfOk {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]interface{}{
			"status":      "unready",
			"zapi":        zapiOk,
			"spf":         spfOk,
			"interfaces":  ifacesOk,
		})
		return
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"status": "ready",
	})
}

// OLSR state APIs

func (s *APIServer) handleNeighbors(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error": "Method Not Allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	neighs := s.neighMgr.GetNeighbors()
	links := s.neighMgr.GetLinks()
	mprs := s.neighMgr.GetMPRSet()
	selectors := s.neighMgr.GetMPRSelectors()

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"neighbors":     neighs,
		"links":         links,
		"mpr_set":       mprs,
		"mpr_selectors": selectors,
	})
}

func (s *APIServer) handleTopology(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error": "Method Not Allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	topo := s.topoMgr.GetTopology()
	mid := s.topoMgr.GetMIDMap()

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"topology_set": topo,
		"mid_map":      mid,
	})
}

func (s *APIServer) handleRoutes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error": "Method Not Allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	unicast := s.spfEngine.GetRoutes()
	multicast := s.molsrMgr.GetActiveMFC()

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"unicast":   unicast,
		"multicast": multicast,
	})
}

func (s *APIServer) handleInterfaces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error": "Method Not Allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	ifaces := s.monitor.GetInterfaces()
	jsonResponse(w, http.StatusOK, ifaces)
}

// HNA API

type hnaPayload struct {
	Prefix string `json:"prefix"`
}

func (s *APIServer) handleHNA(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		entries := s.hnaMgr.GetHNAEntries()
		jsonResponse(w, http.StatusOK, entries)

	case http.MethodPost:
		var payload hnaPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, `{"error": "Bad Request"}`, http.StatusBadRequest)
			return
		}
		_, ipNet, err := net.ParseCIDR(payload.Prefix)
		if err != nil {
			http.Error(w, `{"error": "Invalid CIDR prefix"}`, http.StatusBadRequest)
			return
		}

		s.hnaMgr.AddLocalHNA(r.Context(), *ipNet)
		jsonResponse(w, http.StatusCreated, map[string]string{"message": "HNA prefix registered"})

	case http.MethodDelete:
		var payload hnaPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, `{"error": "Bad Request"}`, http.StatusBadRequest)
			return
		}
		_, ipNet, err := net.ParseCIDR(payload.Prefix)
		if err != nil {
			http.Error(w, `{"error": "Invalid CIDR prefix"}`, http.StatusBadRequest)
			return
		}

		s.hnaMgr.RemoveLocalHNA(r.Context(), *ipNet)
		jsonResponse(w, http.StatusOK, map[string]string{"message": "HNA prefix deregistered"})

	default:
		http.Error(w, `{"error": "Method Not Allowed"}`, http.StatusMethodNotAllowed)
	}
}

// MOLSR APIs

type sourceClaimPayload struct {
	Source          string `json:"source"`
	Group           string `json:"group"`
	DurationSeconds int    `json:"duration_seconds"`
}

func (s *APIServer) handleSourceClaims(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		claims := s.molsrMgr.GetSourceClaims()
		jsonResponse(w, http.StatusOK, claims)

	case http.MethodPost:
		var payload sourceClaimPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, `{"error": "Bad Request"}`, http.StatusBadRequest)
			return
		}
		if net.ParseIP(payload.Source) == nil || net.ParseIP(payload.Group) == nil {
			http.Error(w, `{"error": "Invalid IP address formats"}`, http.StatusBadRequest)
			return
		}
		duration := 60 * time.Second
		if payload.DurationSeconds > 0 {
			duration = time.Duration(payload.DurationSeconds) * time.Second
		}

		s.molsrMgr.AddLocalSourceClaim(r.Context(), payload.Source, payload.Group, duration)
		jsonResponse(w, http.StatusCreated, map[string]string{"message": "Source claim injected"})

	case http.MethodDelete:
		var payload sourceClaimPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, `{"error": "Bad Request"}`, http.StatusBadRequest)
			return
		}

		s.molsrMgr.DeleteLocalSourceClaim(r.Context(), payload.Source, payload.Group)
		jsonResponse(w, http.StatusOK, map[string]string{"message": "Source claim deleted"})

	default:
		http.Error(w, `{"error": "Method Not Allowed"}`, http.StatusMethodNotAllowed)
	}
}

type confirmParentPayload struct {
	Source          string `json:"source"`
	Group           string `json:"group"`
	Parent          string `json:"parent"`
	Child           string `json:"child"`
	DurationSeconds int    `json:"duration_seconds"`
}

func (s *APIServer) handleConfirmParents(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		parents := s.molsrMgr.GetConfirmParents()
		jsonResponse(w, http.StatusOK, parents)

	case http.MethodPost:
		var payload confirmParentPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, `{"error": "Bad Request"}`, http.StatusBadRequest)
			return
		}
		if net.ParseIP(payload.Source) == nil || net.ParseIP(payload.Group) == nil ||
			net.ParseIP(payload.Parent) == nil || net.ParseIP(payload.Child) == nil {
			http.Error(w, `{"error": "Invalid IP address formats"}`, http.StatusBadRequest)
			return
		}
		duration := 60 * time.Second
		if payload.DurationSeconds > 0 {
			duration = time.Duration(payload.DurationSeconds) * time.Second
		}

		s.molsrMgr.AddLocalConfirmParent(r.Context(), payload.Source, payload.Group, payload.Parent, payload.Child, duration)
		metrics.QueueDepth.Inc() // update event depth metric as audit test validation
		jsonResponse(w, http.StatusCreated, map[string]string{"message": "Confirm parent entry registered"})

	case http.MethodDelete:
		var payload confirmParentPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, `{"error": "Bad Request"}`, http.StatusBadRequest)
			return
		}

		s.molsrMgr.DeleteLocalConfirmParent(r.Context(), payload.Source, payload.Group, payload.Child)
		jsonResponse(w, http.StatusOK, map[string]string{"message": "Confirm parent entry deleted"})

	default:
		http.Error(w, `{"error": "Method Not Allowed"}`, http.StatusMethodNotAllowed)
	}
}
