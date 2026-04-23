package dashboard

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"runtime"
	"sync"
	"time"

	"github.com/Automattic/jetmon/internal/config"
	"github.com/Automattic/jetmon/internal/db"
)

// State holds the real-time metrics snapshot served by the dashboard.
type State struct {
	WorkerCount      int       `json:"worker_count"`
	ActiveChecks     int       `json:"active_checks"`
	QueueDepth       int       `json:"queue_depth"`
	RetryQueueSize   int       `json:"retry_queue_size"`
	SitesPerSec      int       `json:"sites_per_sec"`
	RoundDurationMs  int64     `json:"round_duration_ms"`
	WPCOMCircuitOpen bool      `json:"wpcom_circuit_open"`
	WPCOMQueueDepth  int       `json:"wpcom_queue_depth"`
	MemRSSMB         int       `json:"mem_rss_mb"`
	BucketMin        int       `json:"bucket_min"`
	BucketMax        int       `json:"bucket_max"`
	Hostname         string    `json:"hostname"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// HealthEntry represents one external dependency's status.
type HealthEntry struct {
	Name      string    `json:"name"`
	Status    string    `json:"status"` // "green", "amber", "red"
	Latency   int64     `json:"latency_ms,omitempty"`
	LastError string    `json:"last_error,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

// Server is the operator dashboard HTTP server.
type Server struct {
	mu         sync.RWMutex
	state      State
	health     []HealthEntry
	sseClients map[string]chan string
	sseMu      sync.Mutex
	hostname   string

	apiTokens           map[string]struct{}
	apiRateLimitRPS     float64
	apiRateLimitBurst   float64
	apiRateStates       map[string]*clientRateState
	apiRateMu           sync.Mutex
	maxRequestBodyBytes int64

	listSites      func(r *http.Request) ([]db.Site, error)
	getSiteByID    func(r *http.Request, id int64) (db.Site, error)
	createSite     func(r *http.Request, input db.CreateSiteInput) (int64, error)
	patchSite      func(r *http.Request, id int64, input db.PatchSiteInput) (bool, error)
	deleteSite     func(r *http.Request, id int64) (bool, error)
	listSiteEvents func(r *http.Request, siteID int64, limit, offset int) ([]db.SiteEvent, error)
	bucketTotal    int
}

// New creates a new dashboard Server.
func New(hostname string) *Server {
	return NewWithConfig(hostname, nil)
}

// NewWithConfig creates a new dashboard Server with API config.
func NewWithConfig(hostname string, cfg *config.Config) *Server {
	if cfg == nil {
		cfg = &config.Config{}
	}
	tokens := cfg.APITokens
	if len(tokens) == 0 && cfg.AuthToken != "" {
		tokens = []string{cfg.AuthToken}
	}
	tokenSet := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		if t == "" {
			continue
		}
		tokenSet[t] = struct{}{}
	}
	rps := cfg.APIRateLimitRPS
	if rps <= 0 {
		rps = 20
	}
	burst := cfg.APIRateLimitBurst
	if burst <= 0 {
		burst = 40
	}
	bucketTotal := cfg.BucketTotal
	if bucketTotal <= 0 {
		bucketTotal = 1000
	}

	return &Server{
		hostname:            hostname,
		sseClients:          make(map[string]chan string),
		apiTokens:           tokenSet,
		apiRateLimitRPS:     float64(rps),
		apiRateLimitBurst:   float64(burst),
		apiRateStates:       make(map[string]*clientRateState),
		maxRequestBodyBytes: 1 << 20,
		bucketTotal:         bucketTotal,
		listSites: func(r *http.Request) ([]db.Site, error) {
			return db.ListSites(r.Context(), parseListSitesParams(r))
		},
		getSiteByID: func(r *http.Request, id int64) (db.Site, error) {
			return db.GetSiteByID(r.Context(), id)
		},
		createSite: func(r *http.Request, input db.CreateSiteInput) (int64, error) {
			return db.CreateSite(r.Context(), input)
		},
		patchSite: func(r *http.Request, id int64, input db.PatchSiteInput) (bool, error) {
			return db.PatchSite(r.Context(), id, input)
		},
		deleteSite: func(r *http.Request, id int64) (bool, error) {
			return db.DeleteSite(r.Context(), id)
		},
		listSiteEvents: func(r *http.Request, siteID int64, limit, offset int) ([]db.SiteEvent, error) {
			return db.ListSiteEvents(r.Context(), siteID, db.ListSiteEventsParams{Limit: limit, Offset: offset})
		},
	}
}

// Update replaces the current dashboard state and pushes an SSE event.
func (s *Server) Update(st State) {
	st.Hostname = s.hostname
	st.UpdatedAt = time.Now()

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	st.MemRSSMB = int(ms.Sys / 1024 / 1024)

	s.mu.Lock()
	s.state = st
	s.mu.Unlock()

	s.broadcast(st)
}

// UpdateHealth replaces the health entries and pushes an SSE event.
func (s *Server) UpdateHealth(entries []HealthEntry) {
	s.mu.Lock()
	s.health = entries
	s.mu.Unlock()
}

// Listen starts the dashboard HTTP server. Blocks until the server exits.
func (s *Server) Listen(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/events", s.handleSSE)
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.Handle("/api/v1", s.apiMiddleware(http.HandlerFunc(s.handleAPIV1)))
	mux.Handle("/api/v1/", s.apiMiddleware(http.HandlerFunc(s.handleAPIV1)))

	log.Printf("dashboard: listening on %s", addr)
	return http.ListenAndServe(addr, mux)
}

// ListenDebug starts a localhost-only pprof/debug server on the given address.
// pprof can expose in-memory credentials; never expose this on a public interface.
func ListenDebug(addr string) error {
	// net/http/pprof registers itself on http.DefaultServeMux via init().
	log.Printf("debug: pprof listening on %s (localhost only)", addr)
	return http.ListenAndServe(addr, http.DefaultServeMux)
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	st := s.state
	s.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(st)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	h := s.health
	s.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(h)
}

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan string, 16)
	id := fmt.Sprintf("%p", ch)

	s.sseMu.Lock()
	s.sseClients[id] = ch
	s.sseMu.Unlock()

	defer func() {
		s.sseMu.Lock()
		delete(s.sseClients, id)
		s.sseMu.Unlock()
	}()

	// Send current state immediately on connect.
	s.mu.RLock()
	if b, err := json.Marshal(s.state); err == nil {
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	s.mu.RUnlock()

	for {
		select {
		case msg := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) broadcast(st State) {
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	msg := string(b)

	s.sseMu.Lock()
	defer s.sseMu.Unlock()
	for _, ch := range s.sseClients {
		select {
		case ch <- msg:
		default:
			// Slow client — drop the event rather than block.
		}
	}
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, dashboardHTML)
}

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<title>Jetmon 2 — Operator Dashboard</title>
<style>
  body { font-family: monospace; background: #1a1a1a; color: #e0e0e0; margin: 2rem; }
  h1 { color: #7ec8e3; }
  h2 { color: #aaa; font-size: 1rem; margin-top: 2rem; }
  .grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); gap: 1rem; }
  .card { background: #2a2a2a; padding: 1rem; border-radius: 4px; }
  .card .label { font-size: 0.75rem; color: #888; }
  .card .value { font-size: 1.5rem; color: #7ec8e3; margin-top: 0.25rem; }
  .health-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(180px, 1fr)); gap: 0.5rem; margin-top: 1rem; }
  .health-item { padding: 0.5rem 1rem; border-radius: 4px; font-size: 0.85rem; }
  .green  { background: #1a3a1a; border-left: 4px solid #4caf50; }
  .amber  { background: #3a2a00; border-left: 4px solid #ff9800; }
  .red    { background: #3a1a1a; border-left: 4px solid #f44336; }
  #updated { font-size: 0.75rem; color: #555; margin-top: 2rem; }
</style>
</head>
<body>
<h1>Jetmon 2</h1>
<div id="host"></div>

<h2>CHECK POOL</h2>
<div class="grid">
  <div class="card"><div class="label">GOROUTINES</div><div class="value" id="workers">—</div></div>
  <div class="card"><div class="label">ACTIVE CHECKS</div><div class="value" id="active">—</div></div>
  <div class="card"><div class="label">QUEUE DEPTH</div><div class="value" id="queue">—</div></div>
  <div class="card"><div class="label">RETRY QUEUE</div><div class="value" id="retry">—</div></div>
</div>

<h2>THROUGHPUT</h2>
<div class="grid">
  <div class="card"><div class="label">SITES/SEC</div><div class="value" id="sps">—</div></div>
  <div class="card"><div class="label">ROUND TIME</div><div class="value" id="round">—</div></div>
  <div class="card"><div class="label">BUCKETS</div><div class="value" id="buckets">—</div></div>
  <div class="card"><div class="label">RSS</div><div class="value" id="rss">—</div></div>
</div>

<h2>EXTERNAL DEPENDENCIES</h2>
<div class="grid">
  <div class="card"><div class="label">WPCOM CIRCUIT</div><div class="value" id="wpcom">—</div></div>
  <div class="card"><div class="label">WPCOM QUEUE</div><div class="value" id="wpcomq">—</div></div>
</div>

<div id="updated"></div>

<script>
const src = new EventSource('/events');
src.onmessage = function(e) {
  const d = JSON.parse(e.data);
  document.getElementById('host').textContent    = d.hostname;
  document.getElementById('workers').textContent = d.worker_count;
  document.getElementById('active').textContent  = d.active_checks;
  document.getElementById('queue').textContent   = d.queue_depth;
  document.getElementById('retry').textContent   = d.retry_queue_size;
  document.getElementById('sps').textContent     = d.sites_per_sec;
  document.getElementById('round').textContent   = (d.round_duration_ms / 1000).toFixed(1) + 's';
  document.getElementById('buckets').textContent = d.bucket_min + '–' + d.bucket_max;
  document.getElementById('rss').textContent     = d.mem_rss_mb + 'MB';
  document.getElementById('wpcom').textContent   = d.wpcom_circuit_open ? 'OPEN' : 'closed';
  document.getElementById('wpcomq').textContent  = d.wpcom_queue_depth;
  document.getElementById('updated').textContent = 'Updated: ' + new Date(d.updated_at).toLocaleTimeString();
};
</script>
</body>
</html>`
