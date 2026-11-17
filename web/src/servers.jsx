import React, { useEffect, useRef, useState } from "react";
import { fetchProfileDiscovery, enrollHost } from "./discovery-client.js";
import { hostBudgetGuidance, assessBudget, runnerMix, RUNNER_PRESETS } from "./host-budget.js";
import { Usage } from "./usage-bars.jsx";
import { UsageChart, USAGE_DIMENSIONS, formatSampleAge } from "./usage-chart.jsx";

const fmt = (n) => n.toLocaleString();
const date = (value) => new Date(value).toLocaleString();
const total = (value, unit) => (value === undefined ? `unknown ${unit}` : `${fmt(value)} ${unit}`);
export function UsagePanel({ host, readUsage }) {
  const [samples, setSamples] = useState(undefined);
  const [error, setError] = useState("");
  const [dimension, setDimension] = useState(USAGE_DIMENSIONS[0][0]);
  useEffect(() => {
    let cancelled = false;
    const controller = new AbortController();
    setSamples(undefined);
    setError("");
    readUsage(host, { signal: controller.signal })
      .then((history) => { if (!cancelled) setSamples(history); })
      .catch((failure) => { if (!cancelled && !controller.signal.aborted) setError(failure instanceof Error ? failure.message : "Usage history is unavailable."); });
    return () => { cancelled = true; controller.abort(); };
  }, [host, readUsage]);
  const selected = USAGE_DIMENSIONS.find(([key]) => key === dimension);
  return (
    <div className="usage-panel">
      <div className="usage-panel-heading">
        <h3>Measured utilization</h3>
        {samples && samples.length > 0 && (
          <select aria-label="Usage metric" value={dimension} onChange={(event) => setDimension(event.target.value)}>
            {USAGE_DIMENSIONS.map(([key, label, unit]) => <option key={key} value={key}>{label} · {unit}</option>)}
          </select>
        )}
      </div>
      {samples === undefined && !error && <p className="muted">Loading measured usage history…</p>}
      {error && <p className="error" role="status">{error}</p>}
      {samples && samples.length === 0 && (
        <p className="empty">Usage history unavailable: no measured samples exist for this host. A monitoring gap is never drawn as zeros.</p>
      )}
      {samples && samples.length > 0 && (
        <div className="chart">
          <div className="chart-key">
            <span className="dot" aria-hidden="true" />
            <span>{selected[1]} — measured, not allocated</span>
            <span className="muted">Last sample {formatSampleAge(new Date(), samples[samples.length - 1].sampled_at)} · source {samples[samples.length - 1].source}</span>
          </div>
          <UsageChart samples={samples} dimension={selected[0]} label={selected[1]} unit={selected[2]} />
        </div>
      )}
    </div>
  );
}
export function ServerCard({ host, readUsage }) {
  return (
    <article className="card host" data-host={host.id}>
      <div className="host-heading">
        <div className="host-icon" aria-hidden="true">▤</div>
        <div>
          <h3>{host.id}</h3>
          <span className="muted">Configured mode: {host.mode}</span>
        </div>
        <span className={`pill ${host.new_claims_paused ? "amber" : "green"}`}>
          {host.new_claims_paused ? "New claims paused" : "New claims allowed"}
        </span>
      </div>
      <p className="muted">Mode and budget changes stay in <a href="#management">Ledger policy management</a>. They pause new claims only; they never drain runners or release charges.</p>
      <Usage allocation={host} />
      <p className="detected-line">
        <span className="muted">Detected physical totals (last discovery):</span>{" "}
        {host.detected ? (
          <strong>
            CPU {fmt(host.detected.cpu_millis)} millicores · Memory {fmt(host.detected.ram_mib)} MiB · Disk {fmt(host.detected.disk_mib)} MiB · Runners not measured by discovery
          </strong>
        ) : (
          <strong>unknown — no discovery totals recorded for this host</strong>
        )}
      </p>
      {host.cpu_cap_millis ? (
        <p className="muted">CPU fair-use cap: {fmt(host.cpu_cap_millis)} millicores. Display only; admission does not enforce it.</p>
      ) : null}
      {!host.observation_fresh && <p className="drain-warning">Stale or unavailable observation. Health here is the last recorded observation, not live state.</p>}
      <UsagePanel host={host.id} readUsage={readUsage} />
    </article>
  );
}
export function DiscoveryResultView({ result }) {
  const unmanaged = result.guests.filter((guest) => !guest.managed).length;
  const utilization = result.utilization;
  return (
    <div className="discovery-result" role="status">
      <h3>Discovery observation · {result.profile_id}</h3>
      <p className="muted">
        Observed {date(result.observed_at)} · authenticated daemon observation {result.identity.observation_available ? "available" : "unavailable or unsupported"}
        {result.identity.server_version ? ` · version ${result.identity.server_version}` : ""} · stored binding: {result.identity.stored_binding_class}
      </p>
      {result.totals ? (
        <p>
          Physical totals ({result.totals.complete ? "complete" : "incomplete — one or more totals unknown"}): CPU {total(result.totals.cpu_millis, "millicores")} · Memory {total(result.totals.ram_mib, "MiB")} · Disk {total(result.totals.disk_mib, "MiB")}
          {result.totals.disk_mib_used !== undefined ? ` (${fmt(result.totals.disk_mib_used)} MiB used)` : ""}
          {result.totals.storage_pool ? ` · Storage pool ${result.totals.storage_pool}: ${total(result.totals.storage_pool_disk_mib, "MiB")}${result.totals.storage_pool_disk_mib_used !== undefined ? ` (${fmt(result.totals.storage_pool_disk_mib_used)} MiB used)` : ""}` : ""}
        </p>
      ) : (
        <p>Physical totals unknown — the backend did not report them during this run.</p>
      )}
      {utilization ? (
        <p>
          Measured this run ({utilization.source}): CPU {fmt(utilization.cpu_cumulative_millis)} ms cumulative · Memory {fmt(utilization.ram_used_mib)} MiB used · Disk {fmt(utilization.disk_used_mib)} MiB used · {fmt(utilization.guests)} guest(s) contributing
        </p>
      ) : (
        <p>Utilization unknown — the backend did not measure it during this run.</p>
      )}
      <p className="drain-warning">Guest-state CPU is cumulative time since each guest started, not a rate or a percentage. A usage rate needs two consecutive discoveries.</p>
      {unmanaged > 0 && (
        <p className="drain-warning" data-unmanaged={unmanaged}>{unmanaged} unmanaged guest(s) observed without runner-controller attribution: the shared limit may not cover them.</p>
      )}
      <h4>Suggested budget — advisory only, never applied</h4>
      <p className="muted">This API estimate subtracts fixed headroom only. The enrollment planner below refines the proposal with observed workloads and disk usage; neither estimate proves available capacity.</p>
      <p>CPU {fmt(result.suggested_budget.cpu_millis)} millicores · Memory {fmt(result.suggested_budget.ram_mib)} MiB · Disk {fmt(result.suggested_budget.disk_mib)} MiB · Runners {fmt(result.suggested_budget.runners)} (runner count is not derivable from host totals)</p>
      <h4>Observed guests <small className="muted">{result.guests.length}</small></h4>
      {result.guests.length === 0 ? (
        <p className="empty">No guests observed in the bounded inventory.</p>
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr><th>Guest</th><th>Status</th><th>Attribution</th><th>Controller · Pool · Reservation</th><th>CPU · millicores</th><th>Memory · MiB</th><th>Root disk · MiB</th></tr>
            </thead>
            <tbody>
              {result.guests.map((guest) => (
                <tr key={guest.name}>
                  <td><strong>{guest.name}</strong></td>
                  <td>{guest.status}</td>
                  <td>{guest.managed ? "Managed runner" : "Unmanaged"}</td>
                  <td className="muted">{guest.controller || "unknown"}{guest.pool ? ` · ${guest.pool}` : ""}{guest.reservation ? ` · ${guest.reservation}` : ""}</td>
                  <td>{guest.cpu_millis === undefined ? "unknown" : fmt(guest.cpu_millis)}</td>
                  <td>{guest.ram_mib === undefined ? "unknown" : fmt(guest.ram_mib)}</td>
                  <td>{guest.root_disk_mib === undefined ? "unknown" : fmt(guest.root_disk_mib)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {result.warnings.length > 0 && (
        <>
          <h4>Warnings</h4>
          <ul className="warning-list">{result.warnings.map((warning) => <li key={warning}>{warning}</li>)}</ul>
        </>
      )}
      {result.blockers.length > 0 && (
        <>
          <h4>Blockers — no enrollment, activation or qualification is implied</h4>
          <ul className="warning-list">{result.blockers.map((blocker) => <li key={blocker}>{blocker}</li>)}</ul>
        </>
      )}
    </div>
  );
}
export function BudgetPlanner({ result, budget, onChange }) {
  const guidance = hostBudgetGuidance(result);
  const [counts, setCounts] = useState([0, 0, 0]);
  const mix = runnerMix(budget, RUNNER_PRESETS, counts);
  const warnings = assessBudget(result, budget, guidance);
  return <div className="budget-planner">
    <h4>Plan shared runner capacity</h4>
    <p>Starting allowance: 10% per dimension, with minimum headroom of 1 vCPU, 2 GiB RAM and 10 GiB disk (capped at host totals). Observed guest limits are also reserved. Adjust these proposed budgets for your workload.</p>
    <div className="budget-fields">
      {[["cpu_millis", "cpu", "Budget CPU millicores"], ["ram_mib", "ram", "Budget memory MiB"], ["disk_mib", "disk", "Budget disk MiB"], ["runners", "runners", "Budget runners"]].map(([key, name, label]) =>
        <label key={key}>{label}<input aria-label={label} name={name} type="number" min="0" max={Number.MAX_SAFE_INTEGER} step="1" value={budget[key]} onChange={event => onChange({...budget, [key]:event.target.value})} required /></label>)}
    </div>
    <ul className="warning-list">{guidance.warnings.map(warning => <li key={warning}>{warning}</li>)}</ul>
    {warnings.length > 0 && <div role="status"><strong>Budget needs review</strong><ul>{warnings.map(warning => <li key={warning}>{warning}</li>)}</ul></div>}
    <p>Illustrative runner sizes below are planning presets, not saved profiles or provisioned runners. Each maximum is an alternative, not additional capacity. Save custom sizes in Runner-size profiles.</p>
    <div className="table-wrap"><table><thead><tr><th>Size</th><th>CPU / RAM / disk</th><th>Resource-only maximum if used alone</th><th>Planned count</th></tr></thead><tbody>
      {RUNNER_PRESETS.map((profile,index) => <tr key={profile.name}><td>{profile.name}</td><td>{profile.cpu_millis / 1000} vCPU / {profile.ram_mib / 1024} GiB / {profile.disk_mib / 1024} GiB</td><td>{mix.fits ? mix.fits[index] : "unknown"}</td><td><input aria-label={`${profile.name} planned count`} type="number" min="0" step="1" value={counts[index]} onChange={event => setCounts(counts.map((count,i) => i === index ? event.target.value : count))} /></td></tr>)}
    </tbody></table></div>
    <p role="status">{mix.unknown ? "Mix unavailable: enter explicit positive CPU, RAM and disk budgets." : mix.invalid ? "Enter valid whole-number counts." : `Combined mix: ${mix.totals.cpu_millis} millicores, ${mix.totals.ram_mib} MiB RAM, ${mix.totals.disk_mib} MiB disk. ${mix.exceeds.length ? "Exceeds shared budget: " + mix.exceeds.join(", ") : "Within the entered resource budget; not an admission guarantee."}`}</p>
    {Number(budget.runners) < counts.reduce((sum,count) => sum + Number(count),0) && <p className="drain-warning">Planned count exceeds the entered runner count limit.</p>}
    <p>Runner count limit is separate. Set it to cover the desired mix; zero CPU, RAM or runner budget blocks new claims. Planning does not transfer or free existing allocations.</p>
  </div>;
}
function EnrollForm({ result, busy, onSubmit, onCancel }) {
  const [budget, setBudget] = useState(() => {
    const proposed = hostBudgetGuidance(result).budget;
    return {cpu_millis:proposed.cpu_millis ?? "",ram_mib:proposed.ram_mib ?? "",disk_mib:proposed.disk_mib ?? "",runners:0};
  });
  return (
    <form className="enroll-form" onSubmit={(event) => onSubmit(event, result)}>
      <h4>Enroll as a new ledger host</h4>
      <p className="drain-warning">Enrollment records a disabled or draining host budget. It never adopts or rewrites an existing host, and it is not activation: enabling stays a separate reviewed action after qualification.</p>
      <label>New host name<input aria-label="New host name" name="name" defaultValue={result.profile_id} required maxLength={256} /></label>
      <BudgetPlanner result={result} budget={budget} onChange={setBudget} />
      <label>Initial host mode<select aria-label="Initial host mode" name="mode" defaultValue="disabled"><option value="disabled">Disabled: pause new claims</option><option value="draining">Draining</option></select></label>
      <label>Enrollment management token<input aria-label="Enrollment management token" name="token" type="password" autoComplete="off" spellCheck={false} required disabled={busy} /></label>
      <p className="muted">Used once and cleared on submission. Known proposed budgets are prefilled after headroom and observed workload allowance; unknown dimensions remain blank for manual review. The server checks that this discovery review is current, unused and fresh. Complete detected totals are recorded without marking the host healthy.</p>
      <button className="primary" type="submit" disabled={busy}>{busy ? "Enrolling…" : "Enroll host"}</button>
      <button className="quiet" type="button" disabled={busy} onClick={onCancel}>Cancel enrollment</button>
    </form>
  );
}
export function Servers({ data, readOnboarding, readUsage, refresh }) {
  const [profiles, setProfiles] = useState(null);
  const [results, setResults] = useState({});
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const [enrolling, setEnrolling] = useState("");
  const mounted = useRef(true), generation = useRef(0), active = useRef(null), token = useRef(null);
  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; generation.current++; active.current?.abort(); if (token.current) token.current.value = ""; };
  }, []);
  async function loadProfiles() {
    if (active.current) return;
    const current = ++generation.current;
    setBusy(true); setMessage("");
    try {
      const view = await readOnboarding();
      if (mounted.current && current === generation.current) setProfiles(view);
    } catch (error) {
      if (mounted.current && current === generation.current) setMessage(error.message);
    } finally { if (mounted.current && current === generation.current) setBusy(false); }
  }
  async function discover(profile) {
    if (active.current || !token.current) return;
    const credential = token.current.value;
    if (!credential) { setMessage("Enter the management token to authorize this discovery."); return; }
    const controller = new AbortController(); active.current = controller;
    const current = ++generation.current;
    const timeout = setTimeout(() => controller.abort(), 15000);
    setBusy(true); setMessage(""); setEnrolling("");
    try {
      const result = await fetchProfileDiscovery(credential, profile, { signal: controller.signal });
      if (mounted.current && current === generation.current) setResults((previous) => ({ ...previous, [profile]: result }));
    } catch (error) {
      if (mounted.current && current === generation.current) setMessage(error.message);
    } finally {
      clearTimeout(timeout); active.current = null;
      if (token.current) token.current.value = "";
      if (mounted.current && current === generation.current) setBusy(false);
    }
  }
  async function enroll(event, result) {
    event.preventDefault();
    if (active.current) return;
    const form = new FormData(event.currentTarget);
    const credential = String(form.get("token") || "");
    const name = String(form.get("name")).trim();
    const budget = Object.fromEntries(["cpu", "ram", "disk", "runners"].map((key) => [key === "cpu" ? "cpu_millis" : key === "ram" ? "ram_mib" : key === "disk" ? "disk_mib" : "runners", Number(form.get(key))]));
    const controller = new AbortController(); active.current = controller;
    const current = ++generation.current;
    const timeout = setTimeout(() => controller.abort(), 15000);
    setBusy(true); setMessage("");
    try {
      if (!result.review) throw new Error("Discovery review unavailable. Refresh discovery before enrolling.");
      await enrollHost(credential, { name, budget, review: result.review, mode: String(form.get("mode")) }, { signal: controller.signal });
      if (mounted.current && current === generation.current) {
        setEnrolling("");
        setMessage(`Host ${name} enrolled using the reviewed discovery snapshot. It remains paused and unqualified: enabling is a separate reviewed action in Ledger policy management.`);
        await refresh();
      }
    } catch (error) {
      if (mounted.current && current === generation.current) setMessage(error.message);
    } finally {
      clearTimeout(timeout); active.current = null;
      if (mounted.current && current === generation.current) setBusy(false);
    }
  }
  return (
    <section id="servers">
      <div className="section-heading">
        <h2>Servers <small>{data.hosts.length}</small></h2>
        <span className="muted">Detected physical totals, measured utilization and explicit enrollment</span>
      </div>
      <div className="card management" id="server-discovery">
        <h2>Server discovery</h2>
        <p>Read-only discovery runs against allowlisted onboarding profiles. It suggests a budget but never applies one: enrollment below is the only write, and it stays disabled or draining.</p>
        <p className="drain-warning">No discovery observation is physical-hardware, whole-project, parent-quota or capacity qualification.</p>
        {!profiles ? (
          <button className="quiet" type="button" disabled={busy} onClick={loadProfiles}>{busy ? "Working…" : "Load discovery profiles"}</button>
        ) : (
          <div className="discovery-controls">
            {profiles.profiles.length === 0 ? (
              <p className="empty">No allowlisted onboarding profiles are configured on this server.</p>
            ) : (
              <>
                <label>Discovery management token<input aria-label="Discovery management token" ref={token} type="password" autoComplete="off" spellCheck={false} disabled={busy || !data.management_enabled} /></label>
                {!data.management_enabled && <p className="error">Discovery requires the separately authorized management role, which is unavailable on this server.</p>}
                <p className="muted">Used for each explicit run and cleared after it. A read token cannot authorize discovery.</p>
                <ul className="profile-rows">
                  {profiles.profiles.map((entry) => (
                    <li key={entry.id}>
                      <strong>{entry.id}</strong>
                      <button className="quiet" type="button" disabled={busy || !data.management_enabled} onClick={() => discover(entry.id)}>{busy ? "Working…" : `Discover ${entry.id}`}</button>
                      {results[entry.id] && (
                        <div className="profile-result">
                          <DiscoveryResultView result={results[entry.id]} />
                          {enrolling === entry.id ? (
                            <EnrollForm result={results[entry.id]} busy={busy} onSubmit={enroll} onCancel={() => setEnrolling("")} />
                          ) : (
                            <button className="quiet" type="button" disabled={busy || !data.management_enabled} onClick={() => { setEnrolling(entry.id); setMessage(""); }}>Enroll this host</button>
                          )}
                        </div>
                      )}
                    </li>
                  ))}
                </ul>
              </>
            )}
          </div>
        )}
        {message && <p role="status">{message}</p>}
      </div>
      <p className="muted" role="status">Configured mode and budget controls stay in Ledger policy management. Detected totals are discovery display data; admission budgets and fits never read them.</p>
      {data.hosts.length === 0 ? (
        <p className="empty">No hosts configured in this ledger.</p>
      ) : (
        <div className="hosts">
          {data.hosts.map((host) => <ServerCard key={host.id} host={host} readUsage={readUsage} />)}
        </div>
      )}
    </section>
  );
}
