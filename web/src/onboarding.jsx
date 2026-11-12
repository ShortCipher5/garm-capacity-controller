import React, { useEffect, useRef, useState } from "react";
import { probeOnboarding } from "./onboarding-client.js";

export function OnboardingPreflight({ read }) {
  const [discovery, setDiscovery] = useState(null);
  const [profile, setProfile] = useState("");
  const [result, setResult] = useState(null);
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const mounted = useRef(true), generation = useRef(0), active = useRef(null), token = useRef(null);
  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; generation.current++; active.current?.abort(); if (token.current) token.current.value = ""; };
  }, []);
  async function load() {
    if (active.current) return;
    const current = ++generation.current;
    setBusy(true); setMessage(""); setResult(null);
    try {
      const next = await read();
      if (mounted.current && current === generation.current) { setDiscovery(next); setProfile(next.profiles[0]?.id || ""); }
    } catch (error) {
      if (mounted.current && current === generation.current) { setDiscovery(null); setMessage(error.message); }
    } finally { if (mounted.current && current === generation.current) setBusy(false); }
  }
  async function probe(event) {
    event.preventDefault();
    if (active.current || !discovery?.probe_enabled || !profile) return;
    const reason = String(new FormData(event.currentTarget).get("reason")).trim();
    if (!reason || new TextEncoder().encode(reason).length > 256) { setMessage("Use an audit reason of 1–256 bytes."); return; }
    const credential = token.current.value; token.current.value = "";
    const controller = new AbortController(); active.current = controller;
    const current = ++generation.current;
    const timeout = setTimeout(() => controller.abort(), 15000);
    setBusy(true); setMessage(""); setResult(null);
    try {
      const next = await probeOnboarding(credential, { request_id: crypto.randomUUID(), profile_id: profile, reason }, { signal: controller.signal });
      if (mounted.current && current === generation.current) setResult(next);
    } catch (error) { if (mounted.current && current === generation.current) setMessage(error.message); }
    finally { clearTimeout(timeout); active.current = null; if (mounted.current && current === generation.current) setBusy(false); }
  }
  return <section className="card management" id="onboarding-preflight">
    <h2>Incus onboarding preflight</h2>
    <p>Read-only checks of transport profiles allowlisted in private server configuration. There is no browser URL, key, certificate or credential enrollment field.</p>
    <p className="drain-warning">A successful authenticated daemon observation is not physical-hardware, whole-project, parent-quota or capacity qualification. This page cannot enroll or activate a backend, alter budgets, clear legacy exclusions or release resources.</p>
    <button className="quiet" type="button" disabled={busy} onClick={load}>{busy ? "Working…" : discovery ? "Refresh preflight profiles" : "Load preflight profiles"}</button>
    {discovery && <form onSubmit={probe}>
      <label>Allowlisted Incus profile<select aria-label="Allowlisted Incus profile" value={profile} onChange={(event) => { setProfile(event.target.value); setResult(null); }} disabled={busy}>{discovery.profiles.map((entry) => <option key={entry.id} value={entry.id}>{entry.id}</option>)}</select></label>
      <p>Checking profile {profile || "none"}. A profile is not an enrolled host or an independent capacity budget.</p>
      {!discovery.probe_enabled && <p>Probe execution is disabled. Separate management authorization and an explicit server-side probe opt-in are required.</p>}
      <label>Preflight audit reason<input aria-label="Preflight audit reason" name="reason" maxLength="256" required disabled={busy} placeholder="No credentials or sensitive data" /></label>
      <label>Preflight management token<input aria-label="Preflight management token" ref={token} type="password" autoComplete="off" spellCheck={false} required disabled={busy || !discovery.probe_enabled} /></label>
      <p className="muted">Used for this explicit check only and cleared on submission. A read token cannot authorize it. Another explicit probe can perform a new read; request IDs correlate audit, not mutation replay.</p>
      <button className="primary" type="submit" disabled={busy || !profile || !discovery.probe_enabled}>{busy ? "Checking…" : "Run read-only preflight"}</button>
    </form>}
    {result && <div className="preflight-result" role="status">
      <h3>Read-only preflight result</h3>
      <p>Profile {result.profile_id} · {new Date(result.observed_at).toLocaleString()}</p>
      <p>Authenticated daemon observation: {result.observation_available ? "available" : "unavailable or unsupported"}{result.server_version ? ` · version ${result.server_version}` : ""}</p>
      {result.stored_binding_class === "match_observed" ? <p className="error">A stored binding collision was observed. Do not treat this profile as a separate host budget.</p> : result.stored_binding_class === "no_match_observed" ? <p>No stored binding match was observed. This does not prove distinct hardware or complete inventory.</p> : <p>Stored binding comparison was unavailable or not performed.</p>}
      <strong>No enrollment or activation occurred. Capacity remains unqualified.</strong>
      <p>Separate qualified enrollment, scope import and activation workflows are not implemented here. This observation is not a reconciliation fence or provisioning permit.</p>
    </div>}
    {message && <p role="status">{message}</p>}
  </section>;
}
