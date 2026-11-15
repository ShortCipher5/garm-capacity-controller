import { GarmScaleSetInventory } from "./garm-scalesets.jsx";
import React, { useEffect, useRef, useState } from "react";
import { GarmConflictDetails } from "./garm-conflict.jsx";
import { preserveDraftWithLatestInventory, retainGarmDraftConflict, invalidateGarmInventories } from "./garm-observation.js";
import { garmSettingsChanged } from "./garm-sync.js";
import { applyGarmChange } from "./garm-client.js";
import { canEditGarm, GarmSettingsPresentation, GarmPoolInventory } from "./garm-discovery.jsx";

export function GarmManagement({ read }) {
  const [snapshot,setSnapshot] = useState(null), [message,setMessage] = useState(""), [busy,setBusy] = useState(false), [review,setReview] = useState(null), [kind,setKind] = useState("pool"), [target,setTarget] = useState("");
  const [formEpoch,setFormEpoch] = useState(0), [draftDirty,setDraftDirty] = useState(false);
  const [conflictLatest,setConflictLatest] = useState(null);
  const [needsRefresh,setNeedsRefresh] = useState(false), [syncError,setSyncError] = useState(false);
  const dirty = useRef(false), latestLoad = useRef(null);
  const active = useRef(null), token = useRef(null), mounted = useRef(true), uncertain = useRef(false), readGeneration=useRef(0);
  useEffect(() => { mounted.current=true; return () => { mounted.current=false; readGeneration.current++; active.current?.abort(); if(token.current) token.current.value=""; }; }, []);
  async function load(background=false) {
    if(active.current || busy || (background && !snapshot)) return;
    const generation=++readGeneration.current;setBusy(true);
    if(!background){setFormEpoch(value=>value+1);setMessage("");dirty.current=false;setDraftDirty(false);setReview(null);if(token.current)token.current.value="";}
    try {
      const next=await read();
      if(mounted.current && generation===readGeneration.current){
        setSyncError(false);
        if(background && retainGarmDraftConflict(snapshot,next,needsRefresh,Boolean(dirty.current || review),garmSettingsChanged)) {
          setConflictLatest(next);setSnapshot(previous=>preserveDraftWithLatestInventory(previous,next));setNeedsRefresh(true);if(token.current)token.current.value="";
        } else if(!needsRefresh || !background) {
          setSnapshot(next);setNeedsRefresh(false);setConflictLatest(null);
          if(!canEditGarm(next)){setReview(null);if(token.current)token.current.value="";}
          const objects=kind==="pool"?next.settings.pools:next.settings.repositories;
          if(!objects.some(value=>value.id===target))setTarget(objects[0]?.id || "");
        }
      }
    } catch(error) {
      if(mounted.current && generation===readGeneration.current){
        setSyncError(true);if(background)setSnapshot(invalidateGarmInventories);if(token.current)token.current.value="";
        if(!background){setSnapshot(null);setReview(null);setMessage(error.message || "Garm settings unavailable.");}
      }
    } finally {if(mounted.current && generation===readGeneration.current)setBusy(false);}
  }
  latestLoad.current=load;
  useEffect(() => {
    const refresh=()=>{if(document.visibilityState!=="hidden")void latestLoad.current(true);};
    const timer=setInterval(refresh,15000);
    window.addEventListener("focus",refresh);
    document.addEventListener("visibilitychange",refresh);
    return ()=>{clearInterval(timer);window.removeEventListener("focus",refresh);document.removeEventListener("visibilitychange",refresh);};
  }, []);
  function prepare(event) {
    event.preventDefault(); if(needsRefresh || syncError || !canEditGarm(snapshot) || snapshot.write_blocked || uncertain.current)return;
    const form=new FormData(event.currentTarget),selected=(kind==="pool"?snapshot.settings.pools:snapshot.settings.repositories).find((v)=>v.id===target);
    if(!selected)return;
    const change={request_id:crypto.randomUUID(),target,fingerprint:selected.fingerprint,reason:String(form.get("reason")).trim()};
    if(!change.reason||new TextEncoder().encode(change.reason).length>256){setMessage("Use an audit reason of 1–256 bytes.");return;}
    if(kind==="pool") {
      const update={enabled:form.get("enabled")==="true",priority:Number(form.get("priority")),max_runners:Number(form.get("max_runners")),min_idle_runners:Number(form.get("min_idle_runners")),tags:String(form.get("tags")).split(/\r?\n/).map((v)=>v.trim()).filter(Boolean)};
      if(![update.priority,update.max_runners,update.min_idle_runners].every((v)=>Number.isSafeInteger(v)&&v>=0&&v<=1000000)||update.min_idle_runners>update.max_runners||update.min_idle_runners<selected.min_idle_runners){setMessage("Use nonnegative safe integers, maximum at least idle minimum, and do not decrease the idle minimum.");return;}
      change.pool=Object.fromEntries(Object.entries(update).filter(([key,value])=>JSON.stringify(value)!==JSON.stringify(selected[key])));
      if(change.pool.tags && (update.tags.length===0||update.tags.length>100||new Set(update.tags).size!==update.tags.length||update.tags.some((tag)=>new TextEncoder().encode(tag).length>256))){setMessage("Use 1–100 unique future-runner tags, each at most 256 bytes.");return;}
      if(Object.keys(change.pool).length===0){setMessage("No settings differ. Nothing to apply.");return;}
    } else { change.balancer=String(form.get("balancer")); if(change.balancer===selected.pool_balancing_type){setMessage("No settings differ. Nothing to apply.");return;} }
    if(new TextEncoder().encode(JSON.stringify(change)).length>8192){setMessage("This change exceeds the bounded request size. Change fewer tags at once.");return;}
    setReview({kind,change});setMessage("");
  }
  async function apply(event) {
    event.preventDefault();if(active.current || !review || blocked)return;
    const credential=token.current.value;token.current.value="";
    const controller=new AbortController();active.current=controller;setBusy(true);setMessage("");
    const timeout=setTimeout(()=>controller.abort(),15000);
    try {
      await applyGarmChange(credential,review.kind,review.change,{signal:controller.signal});
      if(!mounted.current)return;
      dirty.current=false;setDraftDirty(false);setReview(null);setFormEpoch(value=>value+1);setMessage("Operation recorded as confirmed. Ledger budgets remain independently enforced.");
      try {const next=await read();if(mounted.current)setSnapshot(next);}catch{if(mounted.current){setSnapshot(null);setMessage("Operation recorded as confirmed, but current settings could not be refreshed. Refresh Garm settings before another change.");}}
    } catch(error) { if(mounted.current){setMessage(error.message);if(error.message.includes("Garm settings changed or another change is pending")){setNeedsRefresh(true);setReview(null);}if(error.message.includes("outcome")||error.message.includes("confirmation")){uncertain.current=true;setReview(null);}} }
    finally {clearTimeout(timeout);active.current=null;if(mounted.current)setBusy(false);}
  }
  const choices=snapshot?(kind==="pool"?snapshot.settings.pools:snapshot.settings.repositories):[], selected=choices.find((v)=>v.id===target);
  const stale=review && (review.kind==="pool"?snapshot?.settings.pools:snapshot?.settings.repositories)?.find((v)=>v.id===review.change.target)?.fingerprint!==review.change.fingerprint;
  const blocked=needsRefresh||syncError||busy||!canEditGarm(snapshot)||snapshot?.write_blocked||uncertain.current||stale;
  return <section className="card management" id="garm-management">
    <h2>Garm settings</h2>
    <p>Actual settings on explicitly configured Garm pools and repositories, separate from ledger budgets. No registration, deletion, runner placement or drain controls.</p>
    <p>New demand is ledger-enforced only for separately qualified gateway-routed pools. Repository balancing can affect other eligible pools outside this allowlist; its routing acknowledgement must cover that complete pool/provider graph.</p>
    <p className="drain-warning">Garm v0.2.1 has no atomic conditional updates. A fresh fingerprint check does not fence other Garm UI/CLI writers. Use only with externally coordinated exclusive settings ownership. Tags affect future runners only. Priority is not a fair scheduler. Disabling does not drain idle runners. Decreasing minimum idle is blocked.</p>
    <button className="quiet" onClick={()=>load(false)} disabled={busy}>{busy?"Working…":(needsRefresh||draftDirty||review)?"Discard draft and refresh Garm settings":snapshot?"Refresh Garm settings":"Load Garm settings"}</button>
    <p>Existing Garm settings refresh every 15 seconds while visible and when this window regains focus. No automatic writes or recreation.</p>
    {needsRefresh && <p role="alert" className="error">Garm settings changed elsewhere. Your draft is preserved, but applying it is blocked. Discard the draft and refresh before reviewing a new change.</p>}
    {needsRefresh && conflictLatest && <GarmConflictDetails previous={snapshot} latest={conflictLatest} />}
    {syncError && <p role="alert" className="error">Fresh Garm settings could not be checked. Displayed settings and journal details may be stale; pool and scaleset inventories are unavailable. Editing is blocked until a successful refresh.</p>}
    {snapshot && <GarmPoolInventory inventory={snapshot.pool_inventory} />}
    {snapshot && <GarmScaleSetInventory inventory={snapshot.scaleset_inventory} />}
    {snapshot?.discovery_only && <p>Provider discovery only. No pools are enrolled for management and no management journal or write controls are enabled.</p>}
    {snapshot && !snapshot.discovery_only && !snapshot.settings_available && <p className="error">Remote settings are unavailable. Durable journal status is still shown; editing stays disabled.</p>}
    {snapshot?.write_blocked && <p className="error">A durable pending or unknown operation blocks Garm writes. Reading fresh state does not fence a delayed write and cannot safely clear this block.</p>}
    {snapshot?.pending && <p>Pending request {snapshot.pending.request_id}: {snapshot.pending.kind} / {snapshot.pending.target}</p>}
    {uncertain.current && <p className="error">This session has an uncertain write. Further changes are disabled. Inspect journal status and resolve the external operation before reconnecting.</p>}
    {snapshot && !canEditGarm(snapshot) && <p>Garm editing is unavailable. Discovery only.</p>}
    {snapshot && <div hidden={Boolean(review)}><GarmSettingsPresentation snapshot={snapshot}><form key={formEpoch} onSubmit={prepare} onChange={()=>{dirty.current=true;setDraftDirty(true);}}><fieldset disabled={busy || Boolean(review)} style={{border:0,padding:0,margin:0,minWidth:0}}>
      <label>Garm object type<select aria-label="Garm object type" value={kind} onChange={(e)=>{setKind(e.target.value);setTarget((e.target.value==="pool"?snapshot.settings.pools:snapshot.settings.repositories)[0]?.id||"");}}><option value="pool">Pool settings</option><option value="repository">Repository balancing</option></select></label>
      <label>Garm object<select value={target} onChange={(e)=>setTarget(e.target.value)}>{choices.map((v)=><option key={v.id} value={v.id}>{v.owner?`${v.owner}/${v.name}`:v.id}</option>)}</select></label>
      {selected && (kind==="pool"?<div className="budget-fields" key={selected.fingerprint}>
        <label>Pool enabled<select name="enabled" defaultValue={String(selected.enabled)}><option value="true">Enabled</option><option value="false">Disabled, not drained</option></select></label>
        <label>Pool priority<input name="priority" type="number" min="0" max="1000000" step="1" defaultValue={selected.priority} required /></label>
        <label>Maximum runners<input name="max_runners" type="number" min={selected.min_idle_runners} max="1000000" step="1" defaultValue={selected.max_runners} required /></label>
        <label>Minimum idle runners<input name="min_idle_runners" type="number" min={selected.min_idle_runners} max="1000000" step="1" defaultValue={selected.min_idle_runners} required /></label>
        <label>Future-runner tags, one per line<textarea name="tags" defaultValue={selected.tags.join("\n")} maxLength="8192" /></label>
      </div>:<label key={selected.fingerprint}>Repository balancing<select aria-label="Repository balancing" name="balancer" defaultValue={selected.pool_balancing_type}>{selected.pool_balancing_type===""&&<option value="">Upstream default</option>}<option value="roundrobin">Round robin</option><option value="pack">Pack</option></select></label>)}
      <label>Garm audit reason<input name="reason" maxLength="256" required placeholder="No credentials or sensitive data" /></label>
      <button className="primary" disabled={blocked||!selected} type="submit">Review Garm change</button>
    </fieldset></form></GarmSettingsPresentation></div>}
    {review && canEditGarm(snapshot) && <form onSubmit={apply}>
      <h3>Review actual Garm change</h3><p>{review.kind}: {review.change.target}</p>
      {review.change.pool?Object.entries(review.change.pool).map(([key,value])=><p key={key}>{key}: {Array.isArray(value)?value.join(", "):String(value)}</p>):<p>Repository balancing: {review.change.balancer}</p>}
      {stale && <p className="error">Settings changed after review. Cancel and review a new change.</p>}
      <p>Audit reason: {review.change.reason}</p>
      <label>Garm management write token<input ref={token} type="password" autoComplete="off" spellCheck={false} disabled={busy} required /></label>
      <button className="primary" disabled={blocked} type="submit">{busy?"Applying…":"Apply reviewed Garm change"}</button>
      <button className="quiet" disabled={busy} type="button" onClick={()=>{if(token.current)token.current.value="";setReview(null);setMessage("");}}>Cancel Garm review</button>
    </form>}
    {snapshot?.history_truncated && <p>History is bounded by count and byte size. Older records remain in the journal.</p>}
    {snapshot?.history?.length > 0 && <div className="garm-audit"><h3>Recent Garm changes</h3><p>Up to 25 recent scoped durable operations. Shared management role, not individual operator attribution.</p>{snapshot.history.map((event)=><details key={event.request_id}><summary>{event.state}: {event.kind} / {event.target}</summary><p>{event.reason}</p><p>Request {event.request_id} · {event.created_at}</p><p>Before: {JSON.stringify(event.before)}</p><p>Desired: {JSON.stringify(event.desired)}</p>{event.result && <p>Recorded result: {JSON.stringify(event.result)}</p>}</details>)}</div>}
    {message&&<p role="status">{message}</p>}
  </section>;
}
