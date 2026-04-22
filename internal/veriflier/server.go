package veriflier

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
)

// Server listens for inbound connections from the Monitor and dispatches
// check batches to the local checker. Used by the Veriflier binary.
//
// This is the server-side counterpart to VeriflierClient. It implements
// the same JSON-over-HTTP transport and is replaced by a generated gRPC
// server after running `make generate`.
type Server struct {
	authToken string
	checkFn   func(req CheckRequest) CheckResult
	addr      string
	hostname  string
	version   string
}

// NewServer creates a Server that calls checkFn for each check request.
func NewServer(addr, authToken, hostname, version string, checkFn func(CheckRequest) CheckResult) *Server {
	return &Server{
		addr:      addr,
		authToken: authToken,
		hostname:  hostname,
		version:   version,
		checkFn:   checkFn,
	}
}

// Listen starts the HTTP server. Blocks until the server exits.
func (s *Server) Listen() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/check", s.handleCheck)
	mux.HandleFunc("/status", s.handleStatus)

	log.Printf("veriflier: listening on %s", s.addr)
	return http.ListenAndServe(s.addr, mux)
}

func (s *Server) handleCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token := r.Header.Get("Authorization")
	if token != "Bearer "+s.authToken {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	type batchReq struct {
		Sites []CheckRequest `json:"sites"`
	}
	type batchResp struct {
		Results []CheckResult `json:"results"`
	}

	var req batchReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode: %v", err), http.StatusBadRequest)
		return
	}

	results := make([]CheckResult, 0, len(req.Sites))
	for _, site := range req.Sites {
		res := s.checkFn(site)
		res.Host = s.hostname
		results = append(results, res)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(batchResp{Results: results})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":  "OK",
		"version": s.version,
	})
}
