import React, { useEffect, useRef, useState } from "react";
import { applyPolicyChange } from "./client.js";

const labels = { global_budget: "Global budget", host_budget: "Host budget", host_mode: "Host enable / disable", pool_budget: "Ledger pool budget" };
export function Management({ data, refresh }) {
  const [action, setAction] = useState("host_mode"), [target, setTarget] = useState(data.hosts[0]?.id || ""), [review, setReview] = useState(null), [message, setMessage] = useState(""), [busy, setBusy] = useState(false);
  const token = useRef(null), active = useRef(null), mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; active.current?.abort(); if (token.current) token.current.value = ""; }; }, []);
  const choices = action === "pool_budget" ? data.pools : data.hosts;
  const selected = action === "global_budget" ? data.global : choices.find((v) => v.id === target);
  function changeAction(value) { setAction(value); setTarget((value === "pool_budget" ? data.pools : data.hosts)[0]?.id || ""); setMessage(""); }
  function prepare(event) {
    event.preventDefault();
    const form = new FormData(event.currentTarget), change = { revision: data.policy_revision, action, target: action === "global_budget" ? "" : target, reason: String(form.get("reason")).trim() };
    if (action === "host_mode") change.mode = String(form.get("mode"));
    else {
      change.budget = Object.fromEntries(["cpu_millis", "ram_mib", "runners"].map((key) => [key, Number(form.get(key))]));
      if (!Object.values(change.budget).every((v) => Number.isSafeInteger(v) && v >= 0)) { setMessage("Budgets must be non-negative safe integers."); return; }
    }
    if (!change.reason) { setMessage("A reason is required for the audit trail."); return; }
    setMessage(""); setReview(change);
  }
  async function apply(event) {
    event.preventDefault();
    if (active.current) return;
    const credential = token.current.value; token.current.value = "";
    const controller = new AbortController(); active.current = controller; setBusy(true); setMessage("");
    const timeout = setTimeout(() => controller.abort(), 15000);
    try {
      await applyPolicyChange(credential, review, { signal: controller.signal });
      if (!mounted.current) return;
      setReview(null); setMessage("Policy saved. New claims stay blocked until fresh authoritative reconciliation. Existing resources remain charged.");
      await refresh();
    } catch (error) { if (mounted.current) setMessage(error.message); }
    finally { clearTimeout(timeout); active.current = null; if (mounted.current) setBusy(false); }
  }
  if (!data.management_enabled) return null;
  return <section id="management" className="card management">
    <h2>Ledger policy management</h2>
    <p>Separate write authorization required. These controls change declared admission policy, not Garm pool registration, labels, idle counts or actual runner placement.</p>
    <p className="drain-warning">Disabling pauses new claims only. It does not drain runners, stop jobs or revoke already committed in-flight claims. Enabling requires fresh reconciliation. Lowering a budget never releases existing charges.</p>
    {!review ? <form onSubmit={prepare}>
      <label>Policy control<select value={action} onChange={(e) => changeAction(e.target.value)}>{Object.entries(labels).map(([value, label]) => <option value={value} key={value}>{label}</option>)}</select></label>
      {action !== "global_budget" && <label>Policy target<select value={target} onChange={(e) => setTarget(e.target.value)}>{choices.map((v) => <option key={v.id} value={v.id}>{v.id}</option>)}</select></label>}
      {action === "host_mode" ? <label>New host mode<select name="mode" defaultValue="disabled"><option value="disabled">Disabled: pause new claims</option><option value="enabled">Enabled: require fresh reconciliation</option></select></label> : <div className="budget-fields" key={`${action}:${target}:${data.policy_revision}`}>
        {[["cpu_millis", "Budget CPU millicores"], ["ram_mib", "Budget memory MiB"], ["runners", "Budget runners"]].map(([key, label]) => <label key={key}>{label}<input type="number" name={key} min="0" max={Number.MAX_SAFE_INTEGER} step="1" defaultValue={selected?.budget[key] ?? 0} required /></label>)}
      </div>}
      <label>Audit reason<input name="reason" maxLength="256" required placeholder="No credentials or sensitive data" /></label>
      <button className="primary" type="submit" disabled={!selected}>Review change</button>
    </form> : <form onSubmit={apply}>
      <h3>Review policy change</h3>
      <p>{labels[review.action]}{review.target ? `: ${review.target}` : ""}</p>
      {review.mode ? <p>New mode: {review.mode}</p> : <p>New declared budget: {review.budget.cpu_millis} millicores, {review.budget.ram_mib} MiB, {review.budget.runners} runners</p>}
      <p>Audit reason: {review.reason}</p>
      {review.revision !== data.policy_revision && <p className="error">Policy changed since review. Cancel and review again.</p>}
      <label>Management write token<input ref={token} type="password" autoComplete="off" spellCheck={false} required disabled={busy} /></label>
      <p className="muted">Distinct from the read token. Used once and cleared when submitted. Never stored.</p>
      <button className="primary" type="submit" disabled={busy || review.revision !== data.policy_revision}>{busy ? "Applying…" : "Apply reviewed change"}</button>
      <button className="quiet" type="button" disabled={busy} onClick={() => { if (token.current) token.current.value = ""; setReview(null); setMessage(""); }}>Cancel review</button>
    </form>}
    {message && <p role="status">{message}</p>}
  </section>;
}
