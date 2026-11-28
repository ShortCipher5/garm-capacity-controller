import {ServerGroups} from './server-groups.jsx';
import {AllocatorPlan} from "./allocator-plan.jsx";
import {warmIdleDraftTotal} from './broker-policy-review.js';
import {BrokerPolicyReview} from './broker-policy-review.jsx';
import {BrokerStatus} from "./broker-status.jsx";
import React, { useEffect, useRef, useState } from 'react';
import { saveBrokerPolicy, validateBrokerSaveInput } from './broker-policy-client.js';
export function BrokerPolicy({ readPolicy, readPlan }) {
  const [comparisonStale,setComparisonStale]=useState(false), [savedPolicy,setSavedPolicy]=useState(null);
  const [view,setView]=useState(null), [message,setMessage]=useState(''), [busy,setBusy]=useState(false), [blocked,setBlocked]=useState(true);
  const active=useRef(null), alive=useRef(true), token=useRef(null);
  useEffect(()=>{alive.current=true;return()=>{alive.current=false;active.current?.abort();if(token.current)token.current.value='';};},[]);
  async function load(){
    if(active.current || !readPolicy)return;
    const controller=new AbortController();active.current=controller;setBusy(true);setBlocked(true);setComparisonStale(true);setMessage('');
    try { const next=await readPolicy();if(alive.current&&!controller.signal.aborted){setSavedPolicy(next.policy);setView(next);setComparisonStale(false);setBlocked(false);} }
    catch(e){if(alive.current){setMessage(e.message);setComparisonStale(true);}}
    finally{active.current=null;if(alive.current)setBusy(false);}
  }
  useEffect(()=>{void load();},[]);
  const edit=(fn)=>{setMessage('');setView(v=>({...v,policy:{...v.policy,repositories:fn(v.policy.repositories)}}));};
  const repo=(i,k,value)=>edit(rows=>rows.map((r,j)=>j===i?{...r,[k]:value}:r));
  const pool=(i,j,k,value)=>edit(rows=>rows.map((r,x)=>x===i?{...r,pools:r.pools.map((p,y)=>y===j?{...p,[k]:value}:p)}:r));
  async function save(e){
    e.preventDefault();if(active.current||blocked||view?.writes_enabled!==true)return;
    const credential = token.current?.value || '';
    // Proven pre-dispatch failures must not discard or lock an editable draft.
    try { validateBrokerSaveInput(credential, view.policy); }
    catch (error) { setMessage(error.message); if (token.current) token.current.value = ''; return; }
    const controller=new AbortController();active.current=controller;setBusy(true);setBlocked(true);setComparisonStale(true);
    const timer=setTimeout(()=>controller.abort(),15000);
    try{await saveBrokerPolicy(credential,view.policy,{signal:controller.signal});if(alive.current)setMessage('Capacity policy saved. If the managed cap reconciler is enabled, lower positive pool caps may asynchronously reduce GARM maxima. No runner drain or capacity release is implied. Reload before further edits.');}
    catch(e){if(alive.current){setMessage(e.message);setComparisonStale(true);}}
    finally{clearTimeout(timer);active.current=null;if(token.current)token.current.value='';if(alive.current)setBusy(false);}
  }
  return <section id="broker-policy"><h2>Repository capacity policy</h2><div className="card">
    <p>Saved allocation inputs, not an activation switch. The grant planner consumes saved policy only when its separate runtime prerequisites permit. Saving alone does not enroll pools, reserve capacity, drain runners or change existing charges.</p>
    <p>If the deployment enables managed saved-cap reconciliation, saving a lower positive pool cap may asynchronously lower that GARM pool maximum. It never raises the maximum; zero caps and values below current idle minimums are skipped. <a href="#garm-management">Read actual reconciler status and journal</a> before relying on remote changes. This policy page alone cannot establish whether the service is configured or running.</p>
    <p>Equal fairness uses weight 1. Caps are ceilings, not guarantees; zero means no new capacity. Warm idle is a soft spare-runner target per pool. Pool ownership is not yet verified here.</p>
    <button type="button" disabled={busy||!readPolicy} onClick={load}>Reload policy (discard edits)</button>
    {message&&<p role="status">{message}</p>}
    {view&&<BrokerStatus status={view.status} stale={comparisonStale} />}
    <AllocatorPlan readPlan={readPlan} policyRevision={savedPolicy?.revision} ledgerSequence={view?.status?.sequence} policyStale={comparisonStale} />
    {view&&<form onSubmit={save}>{!view.writes_enabled&&<p>Read-only mode: explore unsaved changes locally. No save is available. Allocation previews still use saved policy, not this draft.</p>}<p>Last loaded revision {savedPolicy?.revision}{comparisonStale ? ' (not verified as current; reload required)' : ''}</p><fieldset disabled={busy||blocked}>
      {view.policy.repositories.map((r,i)=>{const idleTotal=warmIdleDraftTotal(r.pools);return <div className="card" key={i}>
        <h3>Repository row {i + 1}</h3>
        <label>GARM repository UUID<input required value={r.id} onChange={e=>repo(i,'id',e.target.value)}/></label>
        <label>Fairness weight<input type="number" min="1" max="4294967295" required value={r.weight} onChange={e=>repo(i,'weight',e.target.value===''?'':Number(e.target.value))}/></label>
        <label>Repository runner cap<input type="number" min="0" max="1000000" required value={r.max_runners} onChange={e=>repo(i,'max_runners',e.target.value===''?'':Number(e.target.value))}/></label>
        {r.pools.map((p,j)=><div key={j}>
          <h4>Pool row {j + 1}</h4>
          <label>GARM pool UUID<input required value={p.id} onChange={e=>pool(i,j,'id',e.target.value)}/></label>
          <label>Pool runner cap<input type="number" min="0" max={r.max_runners===''?undefined:r.max_runners} required value={p.max_runners} onChange={e=>pool(i,j,'max_runners',e.target.value===''?'':Number(e.target.value))}/></label>
          <label>Warm idle target<input type="number" min="0" max={p.max_runners===''?undefined:p.max_runners} required value={p.warm_idle} onChange={e=>pool(i,j,'warm_idle',e.target.value===''?'':Number(e.target.value))}/></label>
          <button type="button" onClick={()=>repo(i,'pools',r.pools.filter((_,x)=>x!==j))}>Remove pool from draft policy</button>
        </div>)}
        <p>Draft soft pool idle targets combined: {idleTotal ?? 'not available until pool values are valid'}. This is not a guaranteed repository idle floor.</p>
        <button type="button" onClick={()=>repo(i,'pools',[...r.pools,{id:'',max_runners:0,warm_idle:0}])}>Add pool</button>
        <button type="button" onClick={()=>edit(rows=>rows.filter((_,j)=>j!==i))}>Remove repository from draft policy</button>
      </div>;})}
      <button type="button" onClick={()=>edit(rows=>[...rows,{id:'',weight:1,max_runners:0,pools:[]}])}>Add repository</button>
      <ServerGroups view={view} savedPolicy={savedPolicy} stale={comparisonStale} change={groups=>{setMessage('');setView(v=>({...v,policy:{...v.policy,groups}}));}}/>
      <BrokerPolicyReview saved={savedPolicy} draft={view.policy} stale={comparisonStale}/>
      {view.writes_enabled&&<><label>Separate management token<input type="password" autoComplete="off" ref={token}/></label>
      <button type="submit">Save capacity policy</button></>}
    </fieldset></form>}
  </div></section>;
}
