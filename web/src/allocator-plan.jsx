import {StockIdleSuggestions} from './stock-idle-preview.jsx';
import React,{useEffect,useRef,useState} from 'react';
import {allocatorComparisonState,allocatorComparisonMessage,allocatorFailureMessage,allocatorPlanDisplayClock,validateAllocatorPlan,stockMaximumPreviewMessage,stockMaximumPreviewView,stockMaximumBatchPreviewView} from './allocator-plan.js';
// The clock carries across panel remounts in this page, but holds no plan or
// authentication state. A new read cannot reset its prior high-water time.
const displayNow=allocatorPlanDisplayClock();
export function AllocatorPlan({readPlan,policyRevision,ledgerSequence,policyStale}){
 const [view,setView]=useState(null),[error,setError]=useState(''),[busy,setBusy]=useState(false),[now,setNow]=useState(()=>displayNow());
 const active=useRef(null),alive=useRef(true);
 useEffect(()=>{
  alive.current=true;
  const updateTime=()=>setNow(displayNow());
  const timer=setInterval(updateTime,1000);
  document.addEventListener('visibilitychange',updateTime);
  window.addEventListener('focus',updateTime);
  return()=>{alive.current=false;clearInterval(timer);document.removeEventListener('visibilitychange',updateTime);window.removeEventListener('focus',updateTime);active.current?.abort();};
 },[]);
 async function load(){
  if(!readPlan||active.current)return;
  const control=new AbortController();active.current=control;setBusy(true);setView(null);setError('');
  try{
  const v=await readPlan({signal:control.signal});
  if(alive.current&&!control.signal.aborted){
   const received=displayNow();
   // A successful GET may repeat a cached capture. Revalidate it against
   // the same nonextending clock rather than giving it a new display lease.
   setView(validateAllocatorPlan(v,received));
   setNow(received);
  }
 }
  catch(error){if(alive.current)setError(allocatorFailureMessage(error));}
  finally{active.current=null;if(alive.current)setBusy(false);}
 }
 const comparison=allocatorComparisonState(view,{policyRevision,ledgerSequence,policyStale,now});
 return <section aria-label="Observed runners and fresh allocation plan"><h3>Observed runners and fresh allocation plan</h3>
 <p>Read-only demand preview using saved policy. Additional planned runners are advisory, not created runners, a replacement total or a guarantee of GitHub job assignment. No grants, expiry, pool settings or runners are changed by this read.</p>
 <details><summary>Why automatic cross-repository scaling is unavailable here</summary><p>These previews suggest changes but do not apply them. A separately configured cap reconciler can lower existing maxima only; it is not an automatic cross-repository scaler.</p><p>Production scaling needs a verified provider destination, a single settings writer, and a shared capacity check that every runner start must pass. Reusing capacity also needs evidence that the previous runner’s resources were released. Saved mappings and budgets are configuration, not proof that these checks are in place.</p></details>
 <button type="button" disabled={busy||!readPlan} onClick={load}>{busy?'Reading demand and ledger…':'Read fresh allocation plan'}</button>
 {error&&<p role="status">{error}</p>}
 {!view&&<p>Observed runner counts and demand-derived plan: unknown until a successful fresh read.</p>}
 {view&&<><p>{allocatorComparisonMessage(comparison)} Stock reads from {new Date(view.started_at).toLocaleTimeString()} to {new Date(view.finished_at).toLocaleTimeString()}; ledger sequence {view.ledger_sequence}, saved policy revision {view.policy_revision}.</p>
 <p>Stock API reads are not atomic and cannot cover missed webhooks. Unknown stock states are separate from busy, idle and creating. Ledger charges can include grants and unknown resources, not only runners observed here.</p>
 <details><summary>Observation linkage</summary><p>Authenticated source {view.source_identity}</p><p>Demand digest {view.demand_digest}</p></details>
 <div className="table-wrap"><table><thead><tr><th>Pool</th><th>Observed idle / busy / creating / other</th><th>Charged runners</th><th>Saved cap / warm target</th><th>Additional planned / warm portion</th><th>New proposals suppressed by monitoring</th><th>Demand provisionally covered: idle / grants</th><th>Demand deferred for in-flight outcome</th></tr></thead><tbody>{view.pools.map(p=><tr key={p.pool}><td>{p.pool}<p>{p.repository}</p></td><td>{p.supply?['idle','busy','creating','other'].map(k=>p.supply[k]).join(' / '):'Unknown'}</td><td>{p.charged.runners}</td><td>{p.cap} / {p.warm_idle}</td><td>{p.additional} / {p.additional_warm}</td><td>{view.pressure ? (p.suppressed_by_pressure??'Unknown') : 'Not evaluated'}</td><td>{p.covered_by_idle} / {p.covered_by_grant}</td><td>{p.deferred_by_inflight}</td></tr>)}</tbody></table></div>
 {view.pressure&&<details><summary>Partial guest monitoring vetoes</summary><p>High samples against configured budgets can suppress new proposals. These are not physical host totals. Low, missing or stale monitoring never proves headroom; existing charges and pending work remain unchanged.</p><ul>{view.pressure.map(p=><li key={p.host}>{p.host}: {p.state.replaceAll('_',' ')}</li>)}</ul></details>}
 <StockMaximumSuggestions plan={view} comparison={comparison} now={now}/>
 <StockMaximumBatchSuggestions plan={view} comparison={comparison} now={now}/>
 <StockIdleSuggestions plan={view} comparison={comparison} now={now}/>
 <p>Unallocated queue items: {view.remaining_jobs}. Deferred items are not fulfilled jobs. Do not add candidate demand across pools or treat grants as running workers.</p></>}
 </section>;
}


export function StockMaximumSuggestions({plan,comparison,now}){
 const maximums=stockMaximumPreviewView(plan,comparison,now);
 if(!maximums)return null;
 return <section aria-label="Stock pool maximum suggestions"><h4>Stock pool maximum suggestions</h4>
 <p>{stockMaximumPreviewMessage(maximums.state,maximums.reason)}</p>
 {maximums.hostEvidence==="configured_only"&&<p>Configured host only; the provider destination is not verified by this preview.</p>}
 {maximums.state==='proposed'&&<MaximumRows maximums={maximums}/>}
 <p>Advisory only. No settings changes, runner starts, drains or capacity releases are authorized by this preview.</p></section>;
}


export function StockMaximumBatchSuggestions({plan,comparison,now}){
 const batch=stockMaximumBatchPreviewView(plan,comparison,now);
 if(!batch)return null;
 return <details aria-label="Stock maximum batch comparison"><summary>Stock maximum batch comparison (read-only)</summary>
 <p>Compares up to two maximum changes after at least 45 seconds between matching captured observations. It does not monitor applied changes, enforce a cooldown, or queue any action. Provider destination is unverified.</p>
 <p>{stockMaximumPreviewMessage(batch.state,batch.reason)}</p>
 {batch.state==='proposed'&&<MaximumRows maximums={batch}/>}
 </details>;
}

function MaximumRows({maximums}){
 return <><p>Evidence expires {new Date(maximums.expiresAt).toLocaleString()}.</p><div className="table-wrap"><table><thead><tr><th>Repository / pool</th><th>Observed maximum</th><th>Suggested maximum</th></tr></thead><tbody>{maximums.proposals.map(p=><tr key={p.pool_id}><td>{p.repository_id}<p>{p.pool_id}</p>{Object.hasOwn(p,"observed_min_idle")&&<p>Minimum idle: {p.observed_min_idle} (unchanged)</p>}</td><td>{p.observed}</td><td>{p.suggested}</td></tr>)}</tbody></table></div></>;
}
