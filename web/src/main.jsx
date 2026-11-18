import { BrokerPolicy } from "./broker-policy.jsx";
import { createEntityInventoryReader } from "./entity-inventory-client.js";
import React, { useEffect, useRef, useState, useMemo, useCallback } from "react";
import { createRoot } from "react-dom/client";
import { ReadSession, SessionEnded } from "./client.js";
import "./style.css";
import { OnboardingPreflight } from "./onboarding.jsx";
import { GarmManagement } from "./garm-management.jsx";
import { filterHosts } from "./host-filter.js";
import { poolPlacementSummary } from "./pool-placement.js";
import { AuditEvents } from "./audit-events.jsx";
import { Management } from "./management.jsx";
import { Servers } from "./servers.jsx";
import { Profiles } from "./profiles.jsx";
import { Usage, dimensions } from "./usage-bars.jsx";
import { validateProfilesList } from "./profiles-client.js";
import { validateUsageHistory } from "./usage-client.js";

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
function BackendBinding({ status }) {
  if (status === "legacy_scope_import_required") return <p className="error">Legacy charged host scope requires a separate qualified import workflow. This console cannot clear, reset or auto-bind it, even after later cancellation.</p>;
  if (status === "bound_observation") return <p className="muted">Backend binding: authenticated daemon observation only. Not physical-hardware, whole-host capacity or parent-quota qualification.</p>;
  return <p className="drain-warning">Backend unbound. Enabling host mode does not establish a binding or qualify capacity.</p>;
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

const fullDimensions = ["cpu_millis", "ram_mib", "runners"];
function CapacityWarnings({ data }) {
  const full = data.hosts.filter((host) => fullDimensions.some((key) => host.budget[key] > 0 && host.allocated[key] >= host.budget[key]));
  const stale = data.hosts.filter((host) => !host.observation_fresh);
  const unhealthy = data.hosts.filter((host) => !host.last_observed_healthy);
  if (!full.length && !stale.length && !unhealthy.length)
    return <p className="muted" role="status">Capacity warnings: none in this snapshot. Accounted resource states below stay charged until reconciliation proves absence.</p>;
  return (
    <div className="notice" role="status">
      <strong>Capacity warnings in this snapshot</strong>
      {full.length > 0 && <span>At full declared capacity: {full.map((host) => `${host.id} (${fullDimensions.filter((key) => host.budget[key] > 0 && host.allocated[key] >= host.budget[key]).join("/")})`).join(", ")}. Full capacity is not proof of physical exhaustion.</span>}
      {stale.length > 0 && <span>Stale or unavailable observation: {stale.map((host) => host.id).join(", ")}. Health is the last recorded observation.</span>}
      {unhealthy.length > 0 && <span>Last observed unhealthy: {unhealthy.map((host) => host.id).join(", ")}. Unknown or failed resources remain charged.</span>}
    </div>
  );
}
export function Dashboard({ data, busy, refresh, disconnect, readGarm, readOnboarding, readEntityInventory, readProfiles, readUsage, readBrokerPolicy }) {
  const entityReaders = useMemo(() => typeof readEntityInventory === "function" ? createEntityInventoryReader(readEntityInventory) : {}, [readEntityInventory]);
  const [hostFilter, setHostFilter] = useState("all");
  const visibleHosts = filterHosts(data.hosts, hostFilter);
  return (
    <>
      <aside className="sidebar">
        <Brand />
        <div className="nav-label">LEDGER MANAGEMENT</div>
        {[
          ["overview", "Overview"],
          ["hosts", "Hosts"],
          ["servers", "Servers"],
          ["pools", "Pools"],
          ["profiles", "Profiles"],
          ["broker-policy", "Repository policy"],
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
        <CapacityWarnings data={data} />
        {data.legacy_source_preparations > 0 && <div className="notice" role="status"><strong>Original source preparation history unavailable</strong><span>{data.legacy_source_preparations} historical reservations remain charged but cannot be retroactively qualified for creation. Reconciliation, current runner metadata and this console cannot reset or import their original JIT history.</span></div>}
        {data.legacy_unplanned_reservations > 0 && <div className="notice" role="status"><strong>Historical execution plans unavailable</strong><span>{data.legacy_unplanned_reservations} reservations remain charged but lack immutable execution plans. They cannot be created or automatically imported. This console cannot reset or clear their history.</span></div>}
        {data.hosts.some((host) => host.backend_binding_status === "legacy_scope_import_required") && <div className="notice" role="status"><strong>Legacy backend scope import required</strong><span>Previously charged hosts cannot auto-bind, including after later cancellation removes charges. A separate qualified scope-import workflow is required and is not implemented. Mode and budget edits do not clear this exclusion.</span></div>}
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
        {data.garm_management_available && <GarmManagement read={readGarm} {...entityReaders} />}
        {data.onboarding_preflight_available && <OnboardingPreflight read={readOnboarding} />}
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
          <div className="host-filter">
            <label htmlFor="host-attention">Show hosts</label>
            <select id="host-attention" value={hostFilter} onChange={(event) => setHostFilter(event.target.value)}>
              <option value="all">All hosts</option>
              <option value="paused">New claims paused</option>
              <option value="stale">Stale or unavailable observation</option>
              <option value="unhealthy">Last observed unhealthy</option>
            </select>
            <button className="quiet" disabled={hostFilter === "all"} onClick={() => setHostFilter("all")}>Show all hosts</button>
          </div>
          <p className="muted" role="status">Showing {visibleHosts.length} of {data.hosts.length} hosts in this snapshot. Filters do not change budgets or pool placement.</p>
          {data.hosts.length === 0 ? (
            <p className="empty">No hosts configured in this ledger.</p>
          ) : visibleHosts.length === 0 ? (
            <p className="empty">No hosts match this filter.</p>
          ) : (
            <div className="hosts">
              {visibleHosts.map((h) => (
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
                  <BackendBinding status={h.backend_binding_status} />
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
                    <td>{p.hosts?.join(", ") || "None"}<p className="muted">{poolPlacementSummary(p, data.hosts)}</p></td>
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
              Placement diagnostics use this recorded snapshot only, not live health, available budget, pool admission or a permission to provision.
              Priority, labels and idle-target controls are not exposed by this
              read-only API.
            </p>
          </div>
        </section>
        <Servers data={data} readOnboarding={readOnboarding} readUsage={readUsage} refresh={refresh} />
        <Profiles readProfiles={readProfiles} />
        <BrokerPolicy readPolicy={readBrokerPolicy} />
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
          <AuditEvents events={data.events} />
        </div>
        <footer>
          Usage history is measured monitoring data, not allocated capacity. No Garm registration, dynamic placement, provisioning, drain, retirement or resource release is available here.
        </footer>
      </main>
    </>
  );
}
export function App() {
  const session = useRef(new ReadSession());
  const generation = useRef(0);
  const input = useRef(null);
  const [data, setData] = useState(null),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const disconnect = useCallback(() => {
    generation.current++;
    session.current.disconnect();
    if (input.current) input.current.value = "";
    setData(null);
    setBusy(false);
    setError("");
  }, []);
  const readEntityInventory = useCallback(async (options) => {
    try { return await session.current.entityInventorySnapshot(options); }
    catch (error) { if (error instanceof SessionEnded && error.message) { disconnect(); setError(error.message); } throw error; }
  }, [disconnect]);
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
        readOnboarding={async () => {
          try { return await session.current.onboardingSnapshot(); }
          catch (error) { if (error instanceof SessionEnded && error.message) { disconnect(); setError(error.message); } throw error; }
        }}
        readEntityInventory={readEntityInventory}
        readBrokerPolicy={async () => {
          try { return await session.current.brokerPolicySnapshot(); }
          catch (error) { if (error instanceof SessionEnded && error.message) { disconnect(); setError(error.message); } throw error; }
        }}
        readProfiles={async () => {
          try { return await session.current.profilesSnapshot(); }
          catch (error) { if (error instanceof SessionEnded && error.message) { disconnect(); setError(error.message); } throw error; }
        }}
        readUsage={async (host, options) => {
          try { return await session.current.usageHistory(host, options); }
          catch (error) { if (error instanceof SessionEnded && error.message) { disconnect(); setError(error.message); } throw error; }
        }}
        readGarm={async () => {
          try { return await session.current.garmSnapshot(); }
          catch (error) { if (error instanceof SessionEnded && error.message) { disconnect(); setError(error.message); } throw error; }
        }}
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
if (typeof document !== "undefined") {
  createRoot(document.getElementById("root")).render(<App />);
}
