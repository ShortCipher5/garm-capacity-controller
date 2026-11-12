import React, { useEffect, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import { ReadSession, SessionEnded } from "./client.js";
import "./style.css";
import { Management } from "./management.jsx";

const fmt = (n) => n.toLocaleString();
const date = (value) => new Date(value).toLocaleString();
function Brand() {
  return (
    <div className="brand">
      gc
      <span>
        Garm capacity
        <br />
        controller
      </span>
    </div>
  );
}
function Notice() {
  return (
    <div className="notice">
      <strong>Experimental · Not production qualified</strong>
      <span>
        Ledger allocations, not measured CPU or memory utilization. Pausing new
        claims does not stop existing idle runners accepting jobs. No complete
        drain or retirement lifecycle is available here.
      </span>
    </div>
  );
}
const dimensions = [
  ["cpu_millis", "CPU allocation", "millicores"],
  ["ram_mib", "Memory allocation", "MiB"],
  ["runners", "Runner allocation", "runners"],
];
function Usage({ allocation, metric = false }) {
  return dimensions.map(([key, label, unit]) => {
    const used = allocation.allocated[key],
      budget = allocation.budget[key];
    return metric ? (
      <article className="card metric" key={key}>
        <span>{label}</span>
        <h2>
          {fmt(used)}{" "}
          <small>
            / {fmt(budget)} {unit}
          </small>
        </h2>
        <progress
          aria-label={label}
          max={Math.max(budget, 1)}
          value={Math.min(used, Math.max(budget, 1))}
        />
        <p>
          {used > budget
            ? "Over declared budget"
            : `${fmt(budget - used)} ${unit} unallocated`}{" "}
          · Not a guarantee of availability
        </p>
      </article>
    ) : (
      <div className="usage" key={key}>
        <div>
          <span>{label}</span>
          <strong>
            {fmt(used)} / {fmt(budget)} <small>{unit}</small>
            {used > budget ? " · over budget" : ""}
          </strong>
        </div>
        <progress
          aria-label={label}
          max={Math.max(budget, 1)}
          value={Math.min(used, Math.max(budget, 1))}
        />
      </div>
    );
  });
}
function Dashboard({ data, busy, refresh, disconnect }) {
  return (
    <>
      <aside className="sidebar">
        <Brand />
        <div className="nav-label">LEDGER MANAGEMENT</div>
        {[
          ["overview", "Overview"],
          ["hosts", "Hosts"],
          ["pools", "Pools"],
          ["events", "Audit events"],
        ].map(([id, label]) => (
          <a className="nav" key={id} href={`#${id}`}>
            {label}
          </a>
        ))}
        <div className="sidebar-bottom">
          <span className="pill amber">Experimental ledger</span>
          <p>
            Policy edits require a separate management credential.
          </p>
        </div>
      </aside>
      <main id="overview">
        <div className="topline">
          <span>
            Control plane <b>/</b> Ledger snapshot
          </span>
          <button className="quiet" onClick={disconnect}>
            Disconnect
          </button>
        </div>
        <header>
          <div>
            <div className="eyebrow">CAPACITY MANAGEMENT</div>
            <h1>Allocation overview</h1>
            <p className="subtitle">
              Durable reservations and inventory across configured hosts and
              pools.
            </p>
          </div>
          <button className="quiet" disabled={busy} onClick={refresh}>
            {busy ? "Refreshing…" : "Refresh snapshot"}
          </button>
        </header>
        <Notice />
        <div className="snapshot-meta" role="status">
          <span>
            Snapshot {date(data.observed_at)} · Epoch {fmt(data.epoch)}
          </span>
          <span>
            Provisioning{" "}
            {data.experimental_provisioning_enabled
              ? "enabled (experimental)"
              : "off"}{" "}
            · Admission barrier {data.admission_ready ? "ready" : "not ready"}
          </span>
        </div>
        <Management data={data} refresh={refresh} />
        <div className="metrics">
          <Usage allocation={data.global} metric />
        </div>
        <section id="hosts">
          <div className="section-heading">
            <h2>
              Hosts <small>{data.hosts.length}</small>
            </h2>
            <span className="muted">
              Health is the last recorded observation.
            </span>
          </div>
          {data.hosts.length === 0 ? (
            <p className="empty">No hosts configured in this ledger.</p>
          ) : (
            <div className="hosts">
              {data.hosts.map((h) => (
                <article className="card host" key={h.id}>
                  <div className="host-heading">
                    <div className="host-icon" aria-hidden="true">
                      ▤
                    </div>
                    <div>
                      <h3>{h.id}</h3>
                      <span className="muted">Configured mode: {h.mode}</span>
                    </div>
                    <span
                      className={`pill ${h.new_claims_paused ? "amber" : "green"}`}
                    >
                      {h.new_claims_paused
                        ? "New claims paused"
                        : "New claims allowed"}
                    </span>
                  </div>
                  <Usage allocation={h} />
                  <div className="host-footer">
                    <span>
                      Observed{" "}
                      {h.last_observed_healthy ? "healthy" : "unhealthy"} ·{" "}
                      {h.observation_fresh ? "fresh" : "stale / unavailable"}
                    </span>
                    <time dateTime={h.observed_at}>{date(h.observed_at)}</time>
                  </div>
                  {h.new_claims_paused && (
                    <p className="drain-warning">
                      Not drained. Existing idle runners may still receive jobs.
                    </p>
                  )}
                </article>
              ))}
            </div>
          )}
        </section>
        <section id="pools">
          <div className="section-heading">
            <h2>
              Repository pools <small>{data.pools.length}</small>
            </h2>
            <span className="muted">
              Declared allocations / configured budgets
            </span>
          </div>
          <div className="card table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Pool</th>
                  <th>Placement hosts</th>
                  <th>CPU · millicores</th>
                  <th>Memory · MiB</th>
                  <th>Runners</th>
                </tr>
              </thead>
              <tbody>
                {data.pools.map((p) => (
                  <tr key={p.id}>
                    <td>
                      <strong>{p.id}</strong>
                    </td>
                    <td>{p.hosts?.join(", ") || "None"}</td>
                    {dimensions.map(([k]) => (
                      <td key={k}>
                        {fmt(p.allocated[k])} / {fmt(p.budget[k])}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
            {data.pools.length === 0 && (
              <p className="empty">No pools configured in this ledger.</p>
            )}
            <p className="table-note">
              Priority, labels and idle-target controls are not exposed by this
              read-only API.
            </p>
          </div>
        </section>
        <div className="bottom-grid">
          <section>
            <div className="section-heading">
              <h2>Accounted resource states</h2>
            </div>
            <div className="card states">
              {Object.entries(data.states).length ? (
                Object.entries(data.states)
                  .sort(([a], [b]) => a.localeCompare(b))
                  .map(([state, count]) => (
                    <div key={state}>
                      <span>{state}</span>
                      <strong>{fmt(count)}</strong>
                    </div>
                  ))
              ) : (
                <p className="empty">No accounted resources.</p>
              )}
              <p className="table-note">
                Unknown and failed resources remain charged. These counts are
                not live job telemetry.
              </p>
            </div>
          </section>
          <section id="events">
            <div className="section-heading">
              <h2>Audit events</h2>
              <span className="muted">Ledger records, newest first</span>
            </div>
            <div className="card decisions">
              {data.events.length ? (
                [...data.events]
                  .sort((a, b) => b.sequence - a.sequence)
                  .map((e) => (
                    <article className="event" key={e.sequence}>
                      <span className="pill">#{e.sequence}</span>
                      <div>
                        <strong>{e.kind}</strong>
                        <time dateTime={e.at}>{date(e.at)}</time>
                        {e.host && <p>Host: {e.host}</p>}
                        {e.request_key && <p>{e.kind === "admin_policy_change" ? "Policy audit: " : "Request: "}{e.request_key}</p>}
                      </div>
                    </article>
                  ))
              ) : (
                <p className="empty">No audit events recorded.</p>
              )}
            </div>
          </section>
        </div>
        <footer>
          No measured usage history is available. No Garm registration, dynamic placement, provisioning, drain, retirement or resource release is available here.
        </footer>
      </main>
    </>
  );
}
function App() {
  const session = useRef(new ReadSession());
  const generation = useRef(0);
  const input = useRef(null);
  const [data, setData] = useState(null),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const disconnect = () => {
    generation.current++;
    session.current.disconnect();
    if (input.current) input.current.value = "";
    setData(null);
    setBusy(false);
    setError("");
  };
  useEffect(() => {
    window.addEventListener("pagehide", disconnect);
    return () => {
      window.removeEventListener("pagehide", disconnect);
      session.current.disconnect();
    };
  }, []);
  async function load(token) {
    const request = ++generation.current;
    setBusy(true);
    setError("");
    try {
      const next = await (token === undefined
        ? session.current.refresh()
        : session.current.connect(token));
      if (request === generation.current) setData(next);
    } catch (err) {
      if (request === generation.current && !(err instanceof SessionEnded)) {
        setData(null);
        setError(err.message);
      }
    } finally {
      if (request === generation.current) setBusy(false);
    }
  }
  if (data)
    return (
      <Dashboard
        data={data}
        busy={busy}
        refresh={() => load()}
        disconnect={disconnect}
      />
    );
  return (
    <div className="login">
      <div className="card login-card">
        <Brand />
        <div className="eyebrow login-eyebrow">READ-ONLY MANAGEMENT</div>
        <h1>Connect to your ledger</h1>
        <p className="subtitle">
          Use the read token issued by your local admin service. No provider or
          Incus credentials belong here.
        </p>
        <Notice />
        <form
          onSubmit={(e) => {
            e.preventDefault();
            const token = input.current.value;
            input.current.value = "";
            void load(token);
          }}
        >
          <label htmlFor="token">Admin read token</label>
          <input
            ref={input}
            id="token"
            name="read-token"
            type="password"
            autoComplete="off"
            spellCheck={false}
            disabled={busy}
            required
            aria-describedby="token-note"
          />
          <p className="muted" id="token-note">
            64 lowercase hexadecimal characters. Kept only in memory until
            disconnect, error or page exit. Reloading requires reconnection.
          </p>
          <button className="primary" disabled={busy} type="submit">
            {busy ? "Connecting…" : "Connect read-only"}
          </button>
          <button className="quiet" type="button" onClick={disconnect}>
            Cancel / clear
          </button>
        </form>
        {error && (
          <p className="error" role="alert">
            {error}
          </p>
        )}
        <p className="muted">
          This client reads only the same-origin admin API. There is no
          demo-data fallback.
        </p>
      </div>
    </div>
  );
}
createRoot(document.getElementById("root")).render(<App />);
