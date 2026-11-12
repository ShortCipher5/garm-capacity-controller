import React, { useRef, useState } from 'react';
import { createRoot } from 'react-dom/client';
import fixture from './fixture.json';
import { percent, validateOverview } from './model.js';
import './style.css';

function Usage({ title, value, budget, unit = '' }) {
  const ratio = percent(value, budget);
  return <div className="usage"><div><span>{title}</span><strong>{value}{unit} <small>/ {budget}{unit}</small></strong></div><progress value={value} max={budget || 1} aria-label={`${title}: ${value} of ${budget}${unit}`} /><span className="muted">{ratio === null ? 'Unknown' : `${ratio}% allocated`}</span></div>;
}
function Pill({ children, kind = '' }) { return <span className={`pill ${kind}`}>{children}</span>; }
function Dashboard({ data, preview, onExit }) {
  const g = data.global;
  const history = data.history;
  const points = history.map((point, i) => `${20 + i * 93},${150 - point.runners * 15}`).join(' ');
  return <>
    <aside className="sidebar"><a className="brand" href="/" aria-label="Garm Capacity Controller">gc<span>Garm<br />Capacity Controller</span></a><p className="nav-label">CONTROL PLANE</p><a className="nav active" href="#overview"><span aria-hidden="true">▦</span> <span>Overview</span></a><a className="nav" href="#hosts"><span aria-hidden="true">▤</span> <span>Incus hosts</span></a><a className="nav" href="#pools"><span aria-hidden="true">◇</span> <span>Repository pools</span></a><a className="nav" href="#decisions"><span aria-hidden="true">≡</span> <span>Decisions</span></a><div className="sidebar-bottom"><Pill kind="amber">SIMULATION</Pill><p>Above Garm.<br />Not a replacement.</p><a href="https://github.com/ShortCipher5/garm-capacity-controller" target="_blank" rel="noreferrer">Project & documentation ↗</a></div></aside>
    <main id="overview"><div className="topline"><span>WORKSPACE <b>/</b> Synthetic environment</span><button className="quiet" onClick={onExit}>{preview ? 'Exit preview' : 'Disconnect'}</button></div>
      <header><div><p className="eyebrow">ONE BUDGET. EVERY POOL.</p><h1>Capacity overview</h1><p className="subtitle">A single view of allocated capacity across your runner fleet.</p></div><Pill kind="amber">Read-only demo</Pill></header>
      <div className="notice"><strong>Simulation only. No capacity is enforced.</strong><span>Static fixture data. No Garm or Incus connection, no live health checks and no provisioning controls.</span></div>
      <section className="metrics" aria-label="Global allocation"><article className="card metric"><span>CPU allocation</span><h2>{g.allocated.cpu_millis / 1000}<small> / {g.budget.cpu_millis / 1000} cores</small></h2><progress value={g.allocated.cpu_millis} max={g.budget.cpu_millis} aria-label="Global CPU allocated" /><p>Declared millicores, not utilization</p></article><article className="card metric"><span>Memory allocation</span><h2>{g.allocated.ram_mib / 1024}<small> / {g.budget.ram_mib / 1024} GiB</small></h2><progress value={g.allocated.ram_mib} max={g.budget.ram_mib} aria-label="Global RAM allocated" /><p>Reservations remain charged</p></article><article className="card metric"><span>Runner allocation</span><h2>{g.allocated.runners}<small> / {g.budget.runners} runners</small></h2><progress value={g.allocated.runners} max={g.budget.runners} aria-label="Global runner allocation" /><p>Includes idle, busy and unknown</p></article></section>
      <section id="hosts"><div className="section-heading"><h2>Incus hosts <small>{data.hosts.length}</small></h2><span className="muted">Synthetic health · policy is not editable</span></div><div className="hosts">{data.hosts.map(host => <article className="card host" key={host.id}><div className="host-heading"><div className="host-icon">▤</div><div><h3>{host.name}</h3><span className="muted">{host.id} · Synthetic instance data</span></div><Pill kind={host.mode === 'enabled' ? 'green' : 'amber'}>{host.mode}</Pill></div><Usage title="CPU" value={host.allocated.cpu_millis / 1000} budget={host.budget.cpu_millis / 1000} unit=" cores" /><Usage title="Memory" value={host.allocated.ram_mib / 1024} budget={host.budget.ram_mib / 1024} unit=" GiB" /><div className="host-footer"><span>{host.allocated.runners} / {host.budget.runners} runners allocated</span><span>{host.mode === 'draining' ? 'New creates blocked' : 'Admission eligible in model'}</span></div>{host.mode === 'draining' && <p className="drain-warning">Idle runners may still accept jobs. A create block is not a complete drain.</p>}</article>)}</div></section>
      <section id="pools"><div className="section-heading"><h2>Repository pools</h2><span className="muted">Labels and priority are sample policy, not a scheduler</span></div><div className="card table-wrap"><table><thead><tr><th>Repository / labels</th><th>Priority</th><th>Pool max</th><th>Allocated</th><th>State</th></tr></thead><tbody>{data.pools.map(pool => <tr key={pool.repository}><td><strong>{pool.repository}</strong><div className="labels">{pool.labels.map(label => <span key={label}>{label}</span>)}</div></td><td>{pool.priority}</td><td>{pool.max_runners}</td><td>{pool.allocated}</td><td><span className="state-inline">{pool.running} running · {pool.idle} idle{pool.unknown ? ` · ${pool.unknown} unknown` : ''}</span></td></tr>)}</tbody></table><p className="table-note">Pool maxima are not global guarantees. All pools share the global and host budgets.</p></div></section>
      <div className="bottom-grid"><section><div className="section-heading"><h2>Allocation history</h2><Pill>Sample · 1 hour</Pill></div><div className="card chart"><div className="chart-key"><span className="dot" />Allocated runners <span className="muted">Not observed CPU / RAM usage</span></div><svg viewBox="0 0 600 180" role="img" aria-label="Sample allocated runners: 2, 3, 5, 4, 6, 6, 6"><line x1="20" y1="30" x2="578" y2="30" className="grid-line" /><line x1="20" y1="90" x2="578" y2="90" className="grid-line" /><line x1="20" y1="150" x2="578" y2="150" className="grid-line" /><polyline points={points} className="history-line" />{history.map((point, i) => <g key={point.time}><circle cx={20 + i * 93} cy={150 - point.runners * 15} r="4" /><text x={20 + i * 93} y="175" textAnchor="middle">{point.time}</text></g>)}</svg></div></section><section id="decisions"><div className="section-heading"><h2>Recent decisions</h2><span className="muted">Synthetic events</span></div><div className="card decisions">{data.events.map(event => <div className="event" key={event.time}><Pill kind={event.decision === 'reserved' ? 'green' : 'amber'}>{event.decision}</Pill><div><strong>{event.pool}<time>{event.time}</time></strong><p>{event.reason}</p></div></div>)}</div></section></div>
      <footer>Fixture snapshot: {new Date(data.observed_at).toUTCString()} · Experimental · Not production-ready</footer>
    </main>
  </>;
}
function App() {
  const preview = new URLSearchParams(window.location.search).get('preview') === '1';
  const [data, setData] = useState(preview ? validateOverview(fixture) : null);
  const [token, setToken] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const pending = useRef(null);
  const version = useRef(0);
  const cancel = () => { version.current++; pending.current?.abort(); setToken(''); setBusy(false); setError(''); };
  async function connect(event) {
    event.preventDefault();
    if (busy) return;
    const current = ++version.current;
    const controller = new AbortController();
    pending.current = controller;
    setBusy(true); setError('');
    const timer = setTimeout(() => controller.abort(), 8000);
    try {
      const response = await fetch('/api/v1/overview', { headers: { Authorization: `Bearer ${token}` }, signal: controller.signal, cache: 'no-store', redirect: 'error' });
      if (!response.ok) throw new Error(response.status === 401 ? 'That demo token was not accepted.' : 'Demo API is unavailable. No live data is shown.');
      const next = validateOverview(await response.json());
      if (current === version.current) { setData(next); setToken(''); }
    } catch (err) {
      if (current === version.current) { setData(null); setError(err.name === 'AbortError' ? 'Connection timed out. You can try again.' : err.message); }
    } finally {
      clearTimeout(timer);
      if (current === version.current) setBusy(false);
    }
  }
  if (data) return <Dashboard data={data} preview={preview} onExit={() => { cancel(); if (preview) window.location.assign('/'); else setData(null); }} />;
  return <div className="login"><div className="card login-card"><p className="eyebrow">GARM CAPACITY CONTROLLER</p><h1>Meet the control plane.</h1><p className="subtitle">A read-only preview of shared runner capacity.<br />No production integrations are enabled.</p><a className="primary" href="/?preview=1">View synthetic fixture preview →</a><div className="separator">OR CONNECT TO THE LOCAL DEMO API</div><form onSubmit={connect}><label htmlFor="token">Disposable demo token</label><input id="token" name="token" type="password" autoComplete="off" value={token} onChange={event => setToken(event.target.value)} disabled={busy} required minLength={32} placeholder="From CAPACITY_DEMO_TOKEN" /><p className="muted">Use only a throwaway demo token, never a Garm or Incus credential. Kept in memory for this request only.</p><button className="primary" disabled={busy}>{busy ? 'Connecting…' : 'Open read-only demo'}</button>{busy && <button className="quiet" type="button" onClick={cancel}>Cancel</button>}{error && <p className="error" role="alert">{error}</p>}</form><p className="muted">Go API on loopback only · Static fixture data · No writes</p></div></div>;
}
createRoot(document.getElementById('root')).render(<App />);
