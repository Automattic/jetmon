package dashboard

const fleetDashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<title>Jetmon 2 - Fleet Dashboard</title>
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
    margin: 0;
    padding: 24px;
    background: var(--bg);
    color: var(--text);
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  }
  main { max-width: 1500px; margin: 0 auto; }
  h1 { margin: 0; font-size: 1.65rem; }
  h2 { margin: 28px 0 12px; font-size: 0.85rem; color: var(--muted); letter-spacing: 0; text-transform: uppercase; }
  a { color: var(--accent); }
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
  .summary-detail { color: var(--muted); font-size: 0.9rem; line-height: 1.45; }
  .summary-issues { margin: 10px 0 0; padding-left: 18px; font-size: 0.86rem; line-height: 1.45; }
  .summary-issues:empty { display: none; }
  .summary-meta { display: grid; gap: 6px; justify-items: end; color: var(--muted); font-size: 0.8rem; }
  .status-pill {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-width: 72px;
    padding: 5px 9px;
    border-radius: 999px;
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
  table { width: 100%; border-collapse: collapse; background: var(--panel); border: 1px solid var(--line); border-radius: 6px; overflow: hidden; }
  th, td { padding: 9px 10px; border-bottom: 1px solid var(--line); text-align: left; font-size: 0.82rem; vertical-align: top; }
  th { color: var(--muted); text-transform: uppercase; font-size: 0.72rem; background: var(--panel-strong); }
  tr:last-child td { border-bottom: 0; }
  td { overflow-wrap: anywhere; }
  .empty { color: var(--muted); padding: 14px; border: 1px solid var(--line); border-radius: 6px; background: var(--panel); }
  @media (max-width: 760px) {
    body { padding: 14px; }
    .topline, .summary { grid-template-columns: 1fr; }
    .summary-meta { justify-items: start; }
    table { display: block; overflow-x: auto; }
  }
</style>
</head>
<body>
<main>
  <div class="topline">
    <div>
      <h1>Jetmon 2</h1>
      <div class="subtle">Fleet dashboard · <a href="/">host dashboard</a></div>
    </div>
    <span class="status-pill amber" id="summary-pill">loading</span>
  </div>

  <section class="summary amber" id="summary">
    <div>
      <div class="summary-title" id="summary-title">Loading fleet state</div>
      <div class="summary-detail" id="summary-detail">Waiting for process health, bucket ownership, and delivery queue summaries.</div>
      <ul class="summary-issues" id="summary-issues"></ul>
    </div>
    <div class="summary-meta">
      <span id="updated">updated: never</span>
      <span id="counts">processes: -</span>
    </div>
  </section>

  <h2>Fleet Rollup</h2>
  <div class="grid">
    <div class="card"><div class="label">Monitors</div><div class="value" id="monitors">-</div></div>
    <div class="card"><div class="label">Deliverers</div><div class="value" id="deliverers">-</div></div>
    <div class="card"><div class="label">Stale Processes</div><div class="value" id="stale">-</div></div>
    <div class="card"><div class="label">Bucket Coverage</div><div class="value" id="bucket-status">-</div><div class="detail" id="bucket-detail"></div></div>
    <div class="card"><div class="label">Delivery Due</div><div class="value" id="delivery-due">-</div><div class="detail" id="delivery-detail"></div></div>
    <div class="card"><div class="label">Projection Drift</div><div class="value" id="drift">-</div></div>
  </div>

  <h2>Delivery Ownership</h2>
  <div class="grid">
    <div class="card"><div class="label">Posture</div><div class="value" id="delivery-posture">-</div><div class="detail" id="delivery-posture-detail"></div></div>
    <div class="card"><div class="label">Enabled Hosts</div><div class="value" id="delivery-enabled">-</div></div>
    <div class="card"><div class="label">Owner Hosts</div><div class="value" id="delivery-owners">-</div></div>
  </div>

  <h2>Delivery Queues</h2>
  <table>
    <thead>
      <tr><th>Kind</th><th>Pending</th><th>Due</th><th>Future Retry</th><th>Delivered</th><th>Failed</th><th>Abandoned</th><th>Oldest Due</th></tr>
    </thead>
    <tbody id="delivery-tables"></tbody>
  </table>

  <h2>Bucket Owners</h2>
  <table>
    <thead>
      <tr><th>Host</th><th>Range</th><th>Status</th><th>Heartbeat</th></tr>
    </thead>
    <tbody id="bucket-hosts"></tbody>
  </table>

  <h2>Processes</h2>
  <table>
    <thead>
      <tr><th>Process</th><th>Health</th><th>State</th><th>Heartbeat</th><th>Buckets</th><th>Queues</th><th>Memory</th></tr>
    </thead>
    <tbody id="processes"></tbody>
  </table>

  <h2>Dependencies</h2>
  <table>
    <thead>
      <tr><th>Name</th><th>Status</th><th>Green</th><th>Amber</th><th>Red</th><th>Stale</th><th>Last Error</th></tr>
    </thead>
    <tbody id="dependencies"></tbody>
  </table>
</main>

<script>
function setText(id, value) {
  document.getElementById(id).textContent = value === undefined || value === null || value === '' ? '-' : value;
}

function statusClass(status) {
  if (status === 'red' || status === 'amber' || status === 'green') return status;
  return 'amber';
}

function renderSummary(summary, generatedAt, processTotal) {
  const status = statusClass(summary.status);
  const box = document.getElementById('summary');
  box.className = 'summary ' + status;
  const pill = document.getElementById('summary-pill');
  pill.className = 'status-pill ' + status;
  pill.textContent = status;
  setText('summary-title', summary.message || 'fleet status unavailable');
  let detail = 'green=' + (summary.green_processes || 0) + ' amber=' + (summary.amber_processes || 0) + ' red=' + (summary.red_processes || 0);
  if (summary.suggested_next_action) detail += ' · next: ' + summary.suggested_next_action;
  setText('summary-detail', detail);
  setText('updated', 'updated: ' + (generatedAt ? new Date(generatedAt).toLocaleTimeString() : 'never'));
  setText('counts', 'processes: ' + processTotal);
  const issues = document.getElementById('summary-issues');
  issues.textContent = '';
  (summary.issues || []).slice(0, 8).forEach(function(issue) {
    const item = document.createElement('li');
    item.textContent = issue;
    issues.appendChild(item);
  });
}

function row(cells) {
  const tr = document.createElement('tr');
  cells.forEach(function(value) {
    const td = document.createElement('td');
    td.textContent = value === undefined || value === null || value === '' ? '-' : value;
    tr.appendChild(td);
  });
  return tr;
}

function rangeLabel(item) {
  if (item.bucket_min === undefined || item.bucket_max === undefined || item.bucket_min === null || item.bucket_max === null) return item.bucket_ownership || '-';
  return item.bucket_min + '-' + item.bucket_max;
}

function ageLabel(seconds) {
  return (seconds || 0) + 's ago';
}

function render(snapshot) {
  const summary = snapshot.summary || {};
  const processes = snapshot.processes || [];
  renderSummary(summary, snapshot.generated_at, processes.length);
  setText('monitors', summary.monitor_processes || 0);
  setText('deliverers', summary.deliverer_processes || 0);
  setText('stale', summary.stale_processes || 0);
  setText('bucket-status', (snapshot.bucket_coverage || {}).status || '-');
  setText('bucket-detail', ((snapshot.bucket_coverage || {}).mode || '-') + ' · ' + ((snapshot.bucket_coverage || {}).host_count || 0) + ' hosts · ' + ((snapshot.bucket_coverage || {}).error || ''));
  setText('delivery-due', (snapshot.delivery || {}).due_now || 0);
  setText('delivery-detail', 'pending=' + ((snapshot.delivery || {}).pending || 0) + ' failed=' + ((snapshot.delivery || {}).failed_since || 0) + ' abandoned=' + ((snapshot.delivery || {}).abandoned_since || 0));
  setText('drift', (snapshot.projection_drift || {}).count || 0);
  const posture = (snapshot.delivery || {}).posture || {};
  setText('delivery-posture', posture.status || '-');
  setText('delivery-posture-detail', posture.message || '');
  setText('delivery-enabled', (posture.enabled_hosts || []).join(', '));
  setText('delivery-owners', (posture.owner_hosts || []).join(', '));

  const deliveryBody = document.getElementById('delivery-tables');
  deliveryBody.textContent = '';
  ((snapshot.delivery || {}).tables || []).forEach(function(table) {
    deliveryBody.appendChild(row([
      table.kind,
      table.pending || 0,
      table.due_now || 0,
      table.future_retry || 0,
      table.delivered_since || 0,
      table.failed_since || 0,
      table.abandoned_since || 0,
      ageLabel(table.oldest_due_age_sec)
    ]));
  });
  if (((snapshot.delivery || {}).tables || []).length === 0) {
    deliveryBody.appendChild(row(['No delivery queue summaries found', '', '', '', '', '', '', '']));
  }

  const bucketBody = document.getElementById('bucket-hosts');
  bucketBody.textContent = '';
  ((snapshot.bucket_coverage || {}).hosts || []).forEach(function(host) {
    bucketBody.appendChild(row([
      host.host_id,
      rangeLabel(host),
      host.status + (host.stale ? ' stale' : ''),
      ageLabel(host.last_heartbeat_age_sec)
    ]));
  });
  if (((snapshot.bucket_coverage || {}).hosts || []).length === 0) {
    bucketBody.appendChild(row(['No dynamic bucket-owner rows found', '', '', '']));
  }

  const processBody = document.getElementById('processes');
  processBody.textContent = '';
  processes.forEach(function(process) {
    processBody.appendChild(row([
      process.process_id,
      process.health_status + (process.stale ? ' stale' : ''),
      process.state,
      ageLabel(process.last_heartbeat_age_sec),
      rangeLabel(process),
      'active=' + (process.active_checks || 0) + ' queue=' + (process.queue_depth || 0) + ' retry=' + (process.retry_queue_size || 0),
      (process.go_sys_mem_mb || 0) + 'MB'
    ]));
  });
  if (processes.length === 0) {
    processBody.appendChild(row(['No process-health snapshots found', '', '', '', '', '', '']));
  }

  const depBody = document.getElementById('dependencies');
  depBody.textContent = '';
  (snapshot.dependencies || []).forEach(function(dep) {
    depBody.appendChild(row([dep.name, dep.status, dep.green_count, dep.amber_count, dep.red_count, dep.stale_count, dep.last_error]));
  });
  if ((snapshot.dependencies || []).length === 0) {
    depBody.appendChild(row(['No dependency snapshots found', '', '', '', '', '', '']));
  }
}

async function refresh() {
  try {
    const res = await fetch('/api/fleet', { cache: 'no-store' });
    if (!res.ok) throw new Error(await res.text());
    render(await res.json());
  } catch (err) {
    renderSummary({ status: 'red', message: 'fleet dashboard unavailable', issues: [String(err)] }, null, 0);
  }
}

refresh();
setInterval(refresh, 10000);
</script>
</body>
</html>`
