import React, { useEffect, useRef, useState } from "react";
import { readBudget, budgetWarnings } from "./budget-review.js";
import { applyPolicyChange } from "./client.js";

const labels = { global_budget: "Global budget", host_budget: "Host budget", host_mode: "Server lifecycle", host_draft_remove: "Remove unused server enrollment", pool_budget: "Ledger pool budget" };

function activationBlockers(host, data) {
  if (!host || host.mode === "enabled") return [];
  return [
    !data.experimental_provisioning_enabled && "Gateway provisioning is off.",
    !data.admission_ready && "Authoritative inventory has not completed reconciliation.",
    host.backend_binding_status !== "bound_observation" && "An authenticated backend binding is required.",
    (!host.observation_fresh || !host.last_observed_healthy) && "A fresh healthy backend observation is required.",
    !data.pools.some(pool => pool.hosts?.includes(host.id)) && "No ledger pool is configured to place runners here.",
    !(host.budget.cpu_millis > 0 && host.budget.ram_mib > 0 && host.budget.runners > 0) && "Positive CPU, memory and runner budgets are required.",
  ].filter(Boolean);
}

export function Management({ data, refresh, hostRequest }) {
  const [action, setAction] = useState("host_mode"), [target, setTarget] = useState(data.hosts[0]?.id || ""), [review, setReview] = useState(null), [message, setMessage] = useState(""), [busy, setBusy] = useState(false);
  const [budgetDraft,setBudgetDraft] = useState(null);
  const token = useRef(null), active = useRef(null), mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; active.current?.abort(); if (token.current) token.current.value = ""; }; }, []);
  useEffect(() => {
    if (!hostRequest || active.current) return;
    if (token.current) token.current.value = "";
    setAction("host_mode");
    setTarget(hostRequest.host);
    setReview(null);
    setBudgetDraft(null);
    setMessage("");
  }, [hostRequest]);
  const choices = action === "pool_budget" ? data.pools : data.hosts;
  const selected = action === "global_budget" ? data.global : choices.find((v) => v.id === target);
  const blockers = action === "host_mode" ? activationBlockers(selected, data) : [];
  const activationUnavailable = review?.mode === "enabled" && blockers.length > 0;
  const budgetAction = action.endsWith("_budget");
  const draftRemovable = !data.experimental_provisioning_enabled && selected?.mode === "disabled"
    && selected.backend_binding_status === "unbound" && !selected.last_observed_healthy
    && !data.pools.some(pool => pool.hosts?.includes(target)) && data.hosts.length > 1;
  function changeAction(value) {
    const nextChoices = value === "pool_budget" ? data.pools : data.hosts;
    setBudgetDraft(null);
    setAction(value);
    if (!nextChoices.some(choice => choice.id === target)) setTarget(nextChoices[0]?.id || "");
    setMessage("");
  }
  function prepare(event) {
    event.preventDefault();
    const form = new FormData(event.currentTarget), change = { revision: data.policy_revision, action, target: action === "global_budget" ? "" : target, reason: String(form.get("reason")).trim() };
    if (action === "host_mode") {
      change.mode = String(form.get("mode"));
      if (change.mode === "enabled" && blockers.length) return;
    }
    else if (budgetAction) {
      try { change.budget = readBudget(form); } catch (error) { setMessage(error.message); return; }
    }
    if (action === "host_draft_remove" && !draftRemovable) return;
    if (!change.reason) { setMessage("A reason is required for the audit trail."); return; }
    setMessage(""); setReview(change);
  }
  async function apply(event) {
    event.preventDefault();
    if (active.current || activationUnavailable || (review.action === "host_draft_remove" && !draftRemovable)) return;
    const credential = token.current.value; token.current.value = "";
    const controller = new AbortController(); active.current = controller; setBusy(true); setMessage("");
    const timeout = setTimeout(() => controller.abort(), 15000);
    try {
      await applyPolicyChange(credential, review, { signal: controller.signal });
      if (!mounted.current) return;
      setReview(null); setBudgetDraft(null);
      if (review.action === "host_draft_remove") setTarget(data.hosts.find(host => host.id !== review.target)?.id || "");
      setMessage(review.action === "host_draft_remove" ? "Unused discovery enrollment removed. Its alias remains reserved." :
        "Policy saved. Admission changes require fresh authoritative reconciliation. Existing resources remain charged.");
      await refresh();
    } catch (error) { if (mounted.current) setMessage(error.message); }
    finally { clearTimeout(timeout); active.current = null; if (mounted.current) setBusy(false); }
  }
  if (!data.management_enabled) return null;
  return <section id="management" className="card management">
    <h2>Ledger policy management</h2>
    <p>Separate write authorization required. These controls change declared admission policy, not Garm pool registration, labels, idle counts or actual runner placement.</p>
    {action === "host_mode" && selected?.backend_binding_status === "legacy_scope_import_required" && <p className="error">This host has a legacy scope exclusion. A mode change cannot import, reset or clear it. Qualified scope import is not implemented.</p>}
    <p className="drain-warning">Pause and request drain close new ledger claims. Already admitted work can finish and existing idle runners may still receive jobs. Drain confirmation and removal of placed servers require authoritative retirement evidence.</p>
    {blockers.length > 0 && <div aria-label="Server placement prerequisites"><p>Activation is blocked.</p><ul>{blockers.map(blocker => <li key={blocker}>{blocker}</li>)}</ul></div>}
    {!review ? <form onSubmit={prepare} onChange={event=>{if(budgetAction){try{setBudgetDraft({key:`${action}:${target}:${data.policy_revision}`,value:readBudget(new FormData(event.currentTarget))});}catch{setBudgetDraft(null);}}}}>
      <label>Policy control<select value={action} onChange={(e) => changeAction(e.target.value)}>{Object.entries(labels).map(([value, label]) => <option value={value} key={value}>{label}</option>)}</select></label>
      {action !== "global_budget" && <label>Policy target<select value={target} onChange={(e) => {setBudgetDraft(null);setTarget(e.target.value);}}>{choices.map((v) => <option key={v.id} value={v.id}>{v.id}</option>)}</select></label>}
      {action === "host_mode" ? <label>New host mode<select key={target} name="mode" defaultValue="disabled"><option value="disabled">Pause new claims</option><option value="draining">Request drain</option><option value="enabled" disabled={blockers.length > 0}>Enable placement after reconciliation</option></select></label> : budgetAction && <div className="budget-fields" key={`${action}:${target}:${data.policy_revision}`}>
        {[["cpu_millis", "Budget CPU millicores"], ["ram_mib", "Budget memory MiB"], ["disk_mib", "Budget disk MiB"], ["runners", "Budget runners"]].map(([key, label]) => <label key={key}>{label}<input type="number" name={key} min="0" max={Number.MAX_SAFE_INTEGER} step="1" defaultValue={selected?.budget[key] ?? ""} required /></label>)}
      </div>}
      {action === "host_draft_remove" && <p>{draftRemovable
        ? "The ledger checks for retained state before removing an unused discovery enrollment."
        : "Removal is blocked. Only an unused, unbound, disabled discovery enrollment can be removed here."}</p>}
      {budgetAction && <BudgetWarnings selected={selected} budget={budgetDraft?.key === `${action}:${target}:${data.policy_revision}` ? budgetDraft.value : selected?.budget} host={action === "host_budget"} />}
      <label>Audit reason<input name="reason" maxLength="256" required placeholder="No credentials or sensitive data" /></label>
      <button className="primary" type="submit" disabled={!selected || (action === "host_draft_remove" && !draftRemovable)}>Review change</button>
    </form> : <form onSubmit={apply}>
      <h3>Review policy change</h3>
      <p>{labels[review.action]}{review.target ? `: ${review.target}` : ""}</p>
      {review.mode ? <p>New mode: {review.mode}{review.mode === "draining" && " (awaiting retirement evidence)"}</p> : review.budget && <p>New declared budget: {review.budget.cpu_millis} millicores, {review.budget.ram_mib} MiB memory, {review.budget.disk_mib === 0 ? "unconstrained disk" : `${review.budget.disk_mib} MiB disk`}, {review.budget.runners} runners</p>}
      {review.action === "host_draft_remove" && <p>This permanently removes the unused local enrollment and reserves its alias. Any configured connection stays in the connection catalog.</p>}
      {review.budget && <BudgetWarnings selected={selected} budget={review.budget} host={review.action === "host_budget"} />}
      <p>Audit reason: {review.reason}</p>
      {review.revision !== data.policy_revision && <p className="error">Policy changed since review. Cancel and review again.</p>}
      <label>Management write token<input ref={token} type="password" autoComplete="off" spellCheck={false} required disabled={busy} /></label>
      <p className="muted">Distinct from the read token. Used once and cleared when submitted. Never stored.</p>
      <button className="primary" type="submit" disabled={busy || activationUnavailable || (review.action === "host_draft_remove" && !draftRemovable) || review.revision !== data.policy_revision}>{busy ? "Applying…" : "Apply reviewed change"}</button>
      <button className="quiet" type="button" disabled={busy} onClick={() => { if (token.current) token.current.value = ""; setReview(null); setBudgetDraft(null); setMessage(""); }}>Cancel review</button>
    </form>}
    {message && <p role="status">{message}</p>}
  </section>;
}

export function BudgetWarnings({selected,budget,host}) {
 const warnings=budgetWarnings(selected,budget,host);
 return warnings.length ? <div role="status" aria-label="Budget review warnings"><ul>{warnings.map((warning,index)=><li key={index}>{warning}</li>)}</ul></div> : null;
}
