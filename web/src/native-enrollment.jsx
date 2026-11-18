import React,{useEffect,useRef,useState} from 'react';
import {enrollNativeDomains} from './native-setup-client.js';
export function NativeEnrollment({read,onRecorded}){
 const [view,setView]=useState(null),[selected,setSelected]=useState(''),[approved,setApproved]=useState(false),[busy,setBusy]=useState(false),[message,setMessage]=useState('');
 const token=useRef(null),active=useRef(null),version=useRef(0),alive=useRef(true);
 useEffect(()=>{alive.current=true;return()=>{alive.current=false;version.current++;active.current?.abort();if(token.current)token.current.value='';};},[]);
 function clear(){version.current++;active.current?.abort();active.current=null;if(token.current)token.current.value='';setView(null);setSelected('');setApproved(false);setBusy(false);setMessage('View cleared. Cancellation does not undo committed native bindings. Reload recorded enrollment before another action.');}
 async function load(){
  if(active.current||!read)return;
  const n=++version.current,control=new AbortController();active.current=control;setBusy(true);setView(null);setSelected('');setApproved(false);setMessage('');
  try{const next=await read({signal:control.signal});if(alive.current&&n===version.current){setView(next);setMessage('Loaded deployment recipes and recorded native bindings. This status read did not contact native owners or authorize readiness.');}}
  catch{if(alive.current&&n===version.current)setMessage('Native enrollment status is unavailable or not configured. No enrollment action can be reviewed until a fresh status read succeeds.');}
  finally{if(alive.current&&n===version.current){active.current=null;setBusy(false);}}
 }
 const choice=view?.recipes.find(r=>r.id===selected);
 async function enroll(){
  if(active.current||!choice||!approved||!view?.writes_enabled)return;
  const credential=token.current?.value||'';if(token.current)token.current.value='';
  if(!/^[a-f0-9]{64}$/.test(credential)){setMessage('Enter the separate native enrollment management token. No request was sent.');return;}
  const expected=view.ledger_sequence,n=++version.current,control=new AbortController();active.current=control;setBusy(true);setView(null);setSelected('');setApproved(false);setMessage('Enrollment request pending. No success or readiness is implied until a correlated response arrives.');
  const timer=setTimeout(()=>control.abort(),45000);
  try{
   const result=await enrollNativeDomains(credential,choice,expected,{signal:control.signal});
   if(!alive.current||n!==version.current)return;
   setMessage(result.outcome==='confirmed'?`Recorded ${result.confirmed_domains} authenticated domain enrollment(s) for the reviewed recipe. Configuration only: readiness remains unauthorized. Reload enrollment status before any further action.`:`Enrollment did not fully confirm. ${result.confirmed_domains} domain enrollment(s) were reported confirmed; other effects may be unknown. Reload recorded enrollments, inspect the configuration, and review a new request. No automatic retry was made.`);
   try{await onRecorded?.();}catch{if(alive.current&&n===version.current)setMessage('Enrollment response received, but capacity overview refresh failed. Reload recorded enrollment status; readiness is not authorized.');}
  }catch(e){if(alive.current&&n===version.current)setMessage(e.message);}
  finally{clearTimeout(timer);if(alive.current&&n===version.current){active.current=null;setBusy(false);}}
 }
 return <details className="card management" onToggle={e=>{if(!e.currentTarget.open)clear();}}><summary>Enroll authenticated native domains</summary>
 <p>Record actual pinned native-owner identities using private deployment recipes. This is distinct from saving an SSH target or discovering host totals. It does not install the native owner, grant execution authority, mark the ledger ready or enable runners.</p>
 <button type="button" disabled={busy||!read} onClick={load}>Load native enrollment recipes and records</button>
 {view&&<><p>Reviewed ledger sequence {view.ledger_sequence}. Readiness authorized: no.</p>
 <label>Approved native enrollment recipe<select aria-label="Approved native enrollment recipe" value={selected} disabled={busy||!view.writes_enabled} onChange={e=>{setSelected(e.target.value);setApproved(false);}}><option value="">Select configured recipe</option>{view.recipes.map(r=><option key={r.id} value={r.id}>{r.id} · {r.mode}</option>)}</select></label>
 {choice&&<><p>Fixed mode: <strong>{choice.mode==='initial'?'Initial whole-ledger enrollment':'Existing exact host bindings'}</strong>. The browser cannot switch modes.</p>
 {choice.mode==='initial'?<p role="alert">Initial mode requires a wholly unbound, uncharged, not-ready ledger and complete configured-host coverage. It is not a fallback for replacing existing bindings.</p>:<p>Every host must already have the exact independently recorded host binding. Identity changes are rejected, not automatically adopted.</p>}
 <ul>{choice.hosts.map(host=><li key={host}>{host}</li>)}</ul><p>Recipe revision <code>{choice.revision}</code>. Owner endpoints, policies and TLS credentials remain server-side.</p>
 <label><input type="checkbox" aria-label="Approve authenticated native enrollment" checked={approved} disabled={busy||!view.writes_enabled} onChange={e=>setApproved(e.target.checked)}/>I approve recording authenticated native domain bindings for these exact hosts using this fixed mode. This does not authorize readiness or runner execution.</label>
 <label>Native enrollment management token<input aria-label="Native enrollment management token" type="password" autoComplete="off" ref={token} disabled={busy}/></label>
 <button type="button" disabled={busy||!approved||!view.writes_enabled} onClick={enroll}>Record reviewed native enrollment</button></>}
 <h4>Recorded domain bindings</h4><p>Historical ledger records, not continuous host health or permission to create runners.</p>
 {view.enrolled.length?<ul>{view.enrolled.map(e=><li key={e.sequence}>{e.host} · sequence {e.sequence} · domain <code>{e.domain_id}</code></li>)}</ul>:<p>No domain enrollments recorded in this snapshot.</p>}
 {!view.recipes.length&&<p>No approved native enrollment recipes are configured.</p>}</>}
 <button type="button" onClick={clear}>{busy?'Cancel enrollment request view':'Clear native enrollment view'}</button>{message&&<p role="status">{message}</p>}
 </details>;
}
