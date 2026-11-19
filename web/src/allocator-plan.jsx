import React,{useEffect,useRef,useState} from 'react';
import {allocatorPlanStale,allocatorFailureMessage} from './allocator-plan.js';
export function AllocatorPlan({readPlan,policyRevision,policyStale}){
 const [view,setView]=useState(null),[error,setError]=useState(''),[busy,setBusy]=useState(false),[now,setNow]=useState(Date.now());
 const active=useRef(null),alive=useRef(true);
 useEffect(()=>{alive.current=true;const timer=setInterval(()=>setNow(Date.now()),1000);return()=>{alive.current=false;clearInterval(timer);active.current?.abort();};},[]);
 async function load(){
  if(!readPlan||active.current)return;
  const control=new AbortController();active.current=control;setBusy(true);setView(null);setError('');
  try{const v=await readPlan({signal:control.signal});if(alive.current&&!control.signal.aborted){setView(v);setNow(Date.now());}}
  catch(error){if(alive.current)setError(allocatorFailureMessage(error));}
  finally{active.current=null;if(alive.current)setBusy(false);}
 }
 const stale=policyStale||allocatorPlanStale(view,now)||(view&&policyRevision!==view.policy_revision);
 return <section aria-label="Observed runners and fresh allocation plan"><h3>Observed runners and fresh allocation plan</h3>
 <p>Read-only demand preview using saved policy. Additional planned runners are advisory, not created runners, a replacement total or a guarantee of GitHub job assignment. No grants, expiry, pool settings or runners are changed by this read.</p>
 <button type="button" disabled={busy||!readPlan} onClick={load}>{busy?'Reading demand and ledger…':'Read fresh allocation plan'}</button>
 {error&&<p role="status">{error}</p>}
 {!view&&<p>Observed runner counts and demand-derived plan: unknown until a successful fresh read.</p>}
 {view&&<><p>{stale?'Stale comparison: counts and plan below are historical, not current.':'Fresh captured comparison.'} Stock reads from {new Date(view.started_at).toLocaleTimeString()} to {new Date(view.finished_at).toLocaleTimeString()}; ledger sequence {view.ledger_sequence}, saved policy revision {view.policy_revision}.</p>
 <p>Stock API reads are not atomic and cannot cover missed webhooks. Unknown stock states are separate from busy, idle and creating. Ledger charges can include grants and unknown resources, not only runners observed here.</p>
 <details><summary>Observation linkage</summary><p>Authenticated source {view.source_identity}</p><p>Demand digest {view.demand_digest}</p></details>
 <div className="table-wrap"><table><thead><tr><th>Pool</th><th>Observed idle / busy / creating / other</th><th>Charged runners</th><th>Saved cap / warm target</th><th>Additional planned / warm portion</th><th>New proposals suppressed by monitoring</th><th>Demand provisionally covered: idle / grants</th><th>Demand deferred for in-flight outcome</th></tr></thead><tbody>{view.pools.map(p=><tr key={p.pool}><td>{p.pool}<p>{p.repository}</p></td><td>{p.supply?['idle','busy','creating','other'].map(k=>p.supply[k]).join(' / '):'Unknown'}</td><td>{p.charged.runners}</td><td>{p.cap} / {p.warm_idle}</td><td>{p.additional} / {p.additional_warm}</td><td>{view.pressure ? (p.suppressed_by_pressure??'Unknown') : 'Not evaluated'}</td><td>{p.covered_by_idle} / {p.covered_by_grant}</td><td>{p.deferred_by_inflight}</td></tr>)}</tbody></table></div>
 {view.pressure&&<details><summary>Partial guest monitoring vetoes</summary><p>High samples against configured budgets can suppress new proposals. These are not physical host totals. Low, missing or stale monitoring never proves headroom; existing charges and pending work remain unchanged.</p><ul>{view.pressure.map(p=><li key={p.host}>{p.host}: {p.state.replaceAll('_',' ')}</li>)}</ul></details>}
 <p>Unallocated queue items: {view.remaining_jobs}. Deferred items are not fulfilled jobs. Do not add candidate demand across pools or treat grants as running workers.</p></>}
 </section>;
}
