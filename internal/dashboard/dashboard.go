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
)

// State holds the real-time metrics snapshot served by the dashboard.
type State struct {
	WorkerCount                   int       `json:"worker_count"`
	ActiveChecks                  int       `json:"active_checks"`
	QueueDepth                    int       `json:"queue_depth"`
	RetryQueueSize                int       `json:"retry_queue_size"`
	SitesPerSec                   int       `json:"sites_per_sec"`
	RoundDurationMs               int64     `json:"round_duration_ms"`
	WPCOMCircuitOpen              bool      `json:"wpcom_circuit_open"`
	WPCOMQueueDepth               int       `json:"wpcom_queue_depth"`
	MemRSSMB                      int       `json:"mem_rss_mb"`
	BucketMin                     int       `json:"bucket_min"`
	BucketMax                     int       `json:"bucket_max"`
	BucketOwnership               string    `json:"bucket_ownership"`
	LegacyStatusProjectionEnabled bool      `json:"legacy_status_projection_enabled"`
	DeliveryWorkersEnabled        bool      `json:"delivery_workers_enabled"`
	DeliveryOwnerHost             string    `json:"delivery_owner_host"`
	RolloutPreflightCommand       string    `json:"rollout_preflight_command"`
	ProjectionDriftCommand        string    `json:"projection_drift_command"`
	Hostname                      string    `json:"hostname"`
	UpdatedAt                     time.Time `json:"updated_at"`
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
}

// New creates a new dashboard Server.
func New(hostname string) *Server {
	return &Server{
		hostname:   hostname,
		sseClients: make(map[string]chan string),
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

// UpdateHealth replaces the health entries served by /api/health.
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
  .card .value.command { font-size: 0.85rem; line-height: 1.35; overflow-wrap: anywhere; }
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

<h2>ROLLOUT</h2>
<div class="grid">
  <div class="card"><div class="label">OWNERSHIP</div><div class="value" id="ownership">—</div></div>
  <div class="card"><div class="label">LEGACY PROJECTION</div><div class="value" id="projection">—</div></div>
  <div class="card"><div class="label">DELIVERY WORKERS</div><div class="value" id="delivery">—</div></div>
  <div class="card"><div class="label">DELIVERY OWNER</div><div class="value" id="delivery-owner">—</div></div>
  <div class="card"><div class="label">PREFLIGHT</div><div class="value command" id="preflight">—</div></div>
  <div class="card"><div class="label">DRIFT REPORT</div><div class="value command" id="drift">—</div></div>
</div>

<h2>EXTERNAL DEPENDENCIES</h2>
<div class="grid">
  <div class="card"><div class="label">WPCOM CIRCUIT</div><div class="value" id="wpcom">—</div></div>
  <div class="card"><div class="label">WPCOM QUEUE</div><div class="value" id="wpcomq">—</div></div>
</div>
<div class="health-grid" id="health"></div>

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
  document.getElementById('ownership').textContent = d.bucket_ownership || '—';
  document.getElementById('projection').textContent = d.legacy_status_projection_enabled ? 'enabled' : 'disabled';
  document.getElementById('delivery').textContent = d.delivery_workers_enabled ? 'enabled' : 'disabled';
  document.getElementById('delivery-owner').textContent = d.delivery_owner_host || 'unset';
  document.getElementById('preflight').textContent = d.rollout_preflight_command || '—';
  document.getElementById('drift').textContent = d.projection_drift_command || '—';
  document.getElementById('wpcom').textContent   = d.wpcom_circuit_open ? 'OPEN' : 'closed';
  document.getElementById('wpcomq').textContent  = d.wpcom_queue_depth;
  document.getElementById('updated').textContent = 'Updated: ' + new Date(d.updated_at).toLocaleTimeString();
};

async function refreshHealth() {
  try {
    const res = await fetch('/api/health', { cache: 'no-store' });
    const entries = await res.json();
    const box = document.getElementById('health');
    box.textContent = '';
    entries.forEach(function(entry) {
      const item = document.createElement('div');
      item.className = 'health-item ' + (entry.status || 'amber');
      const latency = entry.latency_ms ? ' ' + entry.latency_ms + 'ms' : '';
      const detail = entry.last_error ? ' — ' + entry.last_error : '';
      item.textContent = entry.name + ': ' + entry.status + latency + detail;
      box.appendChild(item);
    });
  } catch (err) {
    const box = document.getElementById('health');
    box.textContent = '';
    const item = document.createElement('div');
    item.className = 'health-item red';
    item.textContent = 'dashboard health: red — ' + err;
    box.appendChild(item);
  }
}
refreshHealth();
setInterval(refreshHealth, 10000);
</script>
</body>
</html>`
