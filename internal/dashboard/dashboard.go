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
	RolloutCutoverCommand         string    `json:"rollout_cutover_command"`
	RolloutActivityCommand        string    `json:"rollout_activity_command"`
	RolloutRollbackCommand        string    `json:"rollout_rollback_command"`
	RolloutStateReportCommand     string    `json:"rollout_state_report_command"`
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

// HostSnapshot is the combined per-host dashboard model. Fleet views can reuse
// the same shape for a single process before aggregating many hosts.
type HostSnapshot struct {
	State   State         `json:"state"`
	Health  []HealthEntry `json:"health"`
	Summary HostSummary   `json:"summary"`
}

// HostSummary gives callers an immediate status without reimplementing the
// dashboard's red/amber/green rules.
type HostSummary struct {
	Status     string `json:"status"`
	Message    string `json:"message"`
	RedCount   int    `json:"red_count"`
	AmberCount int    `json:"amber_count"`
	GreenCount int    `json:"green_count"`
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
	mux.HandleFunc("/api/host", s.handleHost)

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
	h := append([]HealthEntry(nil), s.health...)
	s.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(h)
}

func (s *Server) handleHost(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	st := s.state
	h := append([]HealthEntry(nil), s.health...)
	s.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(HostSnapshot{
		State:   st,
		Health:  h,
		Summary: SummarizeHost(st, h),
	})
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

// SummarizeHost reduces local state and dependency health into a dashboard
// status. It deliberately stays simple: red blocks rollout, amber needs
// operator attention, green means no local blocker is visible.
func SummarizeHost(st State, health []HealthEntry) HostSummary {
	summary := HostSummary{Status: "green", Message: "host checks are green"}
	if st.UpdatedAt.IsZero() {
		summary.Status = "amber"
		summary.Message = "waiting for host state"
	}
	wpcomAlreadyRed := false
	for _, entry := range health {
		switch entry.Status {
		case "red":
			summary.RedCount++
			if entry.Name == "wpcom" {
				wpcomAlreadyRed = true
			}
		case "amber":
			summary.AmberCount++
		case "green":
			summary.GreenCount++
		}
	}
	if st.WPCOMCircuitOpen && !wpcomAlreadyRed {
		summary.RedCount++
	}
	if st.DeliveryWorkersEnabled && st.DeliveryOwnerHost == "" {
		summary.AmberCount++
	}
	switch {
	case summary.RedCount > 0:
		summary.Status = "red"
		summary.Message = "rollout-blocking dependency or circuit issue"
	case summary.AmberCount > 0:
		summary.Status = "amber"
		summary.Message = "operator attention needed before rollout"
	case summary.Status == "green":
		summary.Message = "host checks are green"
	}
	return summary
}

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<title>Jetmon 2 - Host Dashboard</title>
<style>
  :root {
    color-scheme: dark;
    --bg: #101316;
    --panel: #1b2024;
    --panel-strong: #252b30;
    --line: #353d43;
    --text: #eef2f5;
    --muted: #9aa7b0;
    --green: #58c783;
    --green-bg: #14301f;
    --amber: #f0b85a;
    --amber-bg: #342814;
    --red: #f06b64;
    --red-bg: #3b1d1b;
    --accent: #77b7d9;
  }
  * { box-sizing: border-box; }
  body {
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    background: var(--bg);
    color: var(--text);
    margin: 0;
    padding: 24px;
  }
  main { max-width: 1400px; margin: 0 auto; }
  h1 { margin: 0; font-size: 1.65rem; color: var(--text); }
  h2 { margin: 28px 0 12px; font-size: 0.85rem; color: var(--muted); letter-spacing: 0; text-transform: uppercase; }
  .topline { display: flex; align-items: baseline; justify-content: space-between; gap: 16px; margin-bottom: 16px; }
  .subtle { color: var(--muted); font-size: 0.85rem; }
  .summary {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    gap: 16px;
    align-items: center;
    padding: 18px;
    border: 1px solid var(--line);
    border-left-width: 6px;
    border-radius: 6px;
    background: var(--panel);
  }
  .summary.green { border-left-color: var(--green); }
  .summary.amber { border-left-color: var(--amber); }
  .summary.red { border-left-color: var(--red); }
  .summary-title { font-size: 1.25rem; margin-bottom: 6px; }
  .summary-detail { color: var(--muted); font-size: 0.9rem; }
  .summary-meta { display: grid; gap: 6px; justify-items: end; color: var(--muted); font-size: 0.8rem; }
  .status-pill {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-width: 72px;
    padding: 5px 9px;
    border-radius: 999px;
    color: var(--text);
    font-size: 0.78rem;
    text-transform: uppercase;
  }
  .status-pill.green { background: var(--green-bg); color: var(--green); }
  .status-pill.amber { background: var(--amber-bg); color: var(--amber); }
  .status-pill.red { background: var(--red-bg); color: var(--red); }
  .grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(210px, 1fr)); gap: 12px; }
  .card { background: var(--panel); padding: 14px; border: 1px solid var(--line); border-radius: 6px; min-height: 78px; }
  .card .label { font-size: 0.72rem; color: var(--muted); text-transform: uppercase; }
  .card .value { font-size: 1.35rem; color: var(--accent); margin-top: 8px; overflow-wrap: anywhere; }
  .card .detail { color: var(--muted); font-size: 0.78rem; margin-top: 6px; overflow-wrap: anywhere; }
  .command-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(320px, 1fr)); gap: 10px; }
  .command { background: var(--panel-strong); border: 1px solid var(--line); border-radius: 6px; padding: 12px; min-width: 0; }
  .command .label { color: var(--muted); font-size: 0.72rem; text-transform: uppercase; margin-bottom: 8px; }
  .command code { color: var(--accent); white-space: normal; overflow-wrap: anywhere; line-height: 1.35; }
  .health-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(230px, 1fr)); gap: 10px; }
  .health-item { padding: 11px 12px; border: 1px solid var(--line); border-left-width: 5px; border-radius: 6px; font-size: 0.83rem; overflow-wrap: anywhere; background: var(--panel); }
  .health-item.green { border-left-color: var(--green); }
  .health-item.amber { border-left-color: var(--amber); }
  .health-item.red { border-left-color: var(--red); }
  @media (max-width: 720px) {
    body { padding: 14px; }
    .topline, .summary { grid-template-columns: 1fr; }
    .summary-meta { justify-items: start; }
    .command-grid { grid-template-columns: 1fr; }
  }
</style>
</head>
<body>
<main>
  <div class="topline">
    <div>
      <h1>Jetmon 2</h1>
      <div class="subtle">Host dashboard</div>
    </div>
    <span class="status-pill amber" id="summary-pill">waiting</span>
  </div>

  <section class="summary amber" id="summary">
    <div>
      <div class="summary-title" id="summary-title">Waiting for host state</div>
      <div class="summary-detail" id="summary-detail">No dashboard update has been received yet.</div>
    </div>
    <div class="summary-meta">
      <span id="host">host: unknown</span>
      <span id="updated">updated: never</span>
    </div>
  </section>

  <h2>Check Pool</h2>
  <div class="grid">
    <div class="card"><div class="label">Goroutines</div><div class="value" id="workers">-</div></div>
    <div class="card"><div class="label">Active Checks</div><div class="value" id="active">-</div></div>
    <div class="card"><div class="label">Queue Depth</div><div class="value" id="queue">-</div></div>
    <div class="card"><div class="label">Retry Queue</div><div class="value" id="retry">-</div></div>
  </div>

  <h2>Throughput</h2>
  <div class="grid">
    <div class="card"><div class="label">Sites/Sec</div><div class="value" id="sps">-</div></div>
    <div class="card"><div class="label">Round Time</div><div class="value" id="round">-</div></div>
    <div class="card"><div class="label">Buckets</div><div class="value" id="buckets">-</div></div>
    <div class="card"><div class="label">RSS</div><div class="value" id="rss">-</div></div>
  </div>

  <h2>Rollout State</h2>
  <div class="grid">
    <div class="card"><div class="label">Ownership</div><div class="value" id="ownership">-</div></div>
    <div class="card"><div class="label">Legacy Projection</div><div class="value" id="projection">-</div></div>
    <div class="card"><div class="label">Delivery Workers</div><div class="value" id="delivery">-</div></div>
    <div class="card"><div class="label">Delivery Owner</div><div class="value" id="delivery-owner">-</div></div>
    <div class="card"><div class="label">WPCOM Circuit</div><div class="value" id="wpcom">-</div></div>
    <div class="card"><div class="label">WPCOM Queue</div><div class="value" id="wpcomq">-</div></div>
  </div>

  <h2>Operator Commands</h2>
  <div class="command-grid">
    <div class="command"><div class="label">State Report</div><code id="state-report">-</code></div>
    <div class="command"><div class="label">Preflight</div><code id="preflight">-</code></div>
    <div class="command"><div class="label">Cutover</div><code id="cutover">-</code></div>
    <div class="command"><div class="label">Activity</div><code id="activity">-</code></div>
    <div class="command"><div class="label">Rollback</div><code id="rollback">-</code></div>
    <div class="command"><div class="label">Drift Report</div><code id="drift">-</code></div>
  </div>

  <h2>External Dependencies</h2>
  <div class="health-grid" id="health"></div>
</main>

<script>
let currentState = null;
let currentHealth = [];

function setText(id, value) {
  document.getElementById(id).textContent = value === undefined || value === null || value === '' ? '-' : value;
}

function renderState(d) {
  currentState = d;
  setText('host', 'host: ' + (d.hostname || 'unknown'));
  setText('workers', d.worker_count);
  setText('active', d.active_checks);
  setText('queue', d.queue_depth);
  setText('retry', d.retry_queue_size);
  setText('sps', d.sites_per_sec);
  setText('round', ((d.round_duration_ms || 0) / 1000).toFixed(1) + 's');
  setText('buckets', d.bucket_min + '-' + d.bucket_max);
  setText('rss', d.mem_rss_mb + 'MB');
  setText('ownership', d.bucket_ownership || '-');
  setText('projection', d.legacy_status_projection_enabled ? 'enabled' : 'disabled');
  setText('delivery', d.delivery_workers_enabled ? 'enabled' : 'disabled');
  setText('delivery-owner', d.delivery_owner_host || 'unset');
  setText('state-report', d.rollout_state_report_command);
  setText('preflight', d.rollout_preflight_command);
  setText('cutover', d.rollout_cutover_command);
  setText('activity', d.rollout_activity_command);
  setText('rollback', d.rollout_rollback_command);
  setText('drift', d.projection_drift_command);
  setText('wpcom', d.wpcom_circuit_open ? 'OPEN' : 'closed');
  setText('wpcomq', d.wpcom_queue_depth);
  setText('updated', 'updated: ' + (d.updated_at ? new Date(d.updated_at).toLocaleTimeString() : 'never'));
  renderSummary();
}

function renderSummary(summary) {
  if (!summary) {
    summary = summarizeLocal();
  }
  const status = summary.status || 'amber';
  const box = document.getElementById('summary');
  box.className = 'summary ' + status;
  const pill = document.getElementById('summary-pill');
  pill.className = 'status-pill ' + status;
  pill.textContent = status;
  setText('summary-title', summary.message || 'host status unavailable');
  setText('summary-detail', 'dependencies green=' + (summary.green_count || 0) + ' amber=' + (summary.amber_count || 0) + ' red=' + (summary.red_count || 0));
}

function summarizeLocal() {
  let red = 0;
  let amber = currentState ? 0 : 1;
  let green = 0;
  let wpcomAlreadyRed = false;
  currentHealth.forEach(function(entry) {
    if (entry.status === 'red') {
      red++;
      if (entry.name === 'wpcom') wpcomAlreadyRed = true;
    }
    else if (entry.status === 'amber') amber++;
    else if (entry.status === 'green') green++;
  });
  if (currentState && currentState.wpcom_circuit_open && !wpcomAlreadyRed) red++;
  if (currentState && currentState.delivery_workers_enabled && !currentState.delivery_owner_host) amber++;
  if (red > 0) return { status: 'red', message: 'rollout-blocking dependency or circuit issue', red_count: red, amber_count: amber, green_count: green };
  if (amber > 0) return { status: 'amber', message: 'operator attention needed before rollout', red_count: red, amber_count: amber, green_count: green };
  return { status: 'green', message: 'host checks are green', red_count: red, amber_count: amber, green_count: green };
}

const src = new EventSource('/events');
src.onmessage = function(e) {
  renderState(JSON.parse(e.data));
};

async function refreshHost() {
  try {
    const res = await fetch('/api/host', { cache: 'no-store' });
    const snapshot = await res.json();
    if (snapshot.state) renderState(snapshot.state);
    const entries = snapshot.health || [];
    currentHealth = entries;
    const box = document.getElementById('health');
    box.textContent = '';
    entries.forEach(function(entry) {
      const item = document.createElement('div');
      item.className = 'health-item ' + (entry.status || 'amber');
      const latency = entry.latency_ms ? ' ' + entry.latency_ms + 'ms' : '';
      const detail = entry.last_error ? ' - ' + entry.last_error : '';
      item.textContent = entry.name + ': ' + entry.status + latency + detail;
      box.appendChild(item);
    });
    renderSummary(snapshot.summary);
  } catch (err) {
    const box = document.getElementById('health');
    box.textContent = '';
    const item = document.createElement('div');
    item.className = 'health-item red';
    item.textContent = 'dashboard health: red - ' + err;
    box.appendChild(item);
    renderSummary({ status: 'red', message: 'dashboard health endpoint failed', red_count: 1, amber_count: 0, green_count: 0 });
  }
}
refreshHost();
setInterval(refreshHost, 10000);
</script>
</body>
</html>`
