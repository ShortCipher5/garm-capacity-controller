import React,{useEffect,useRef,useState} from 'react';
import {startCollection,newCollectionRequestID,validateCollectionOperation} from './native-setup-client.js';
const stateText={pending:'Pending in the collector process',complete:'Collection receipt complete',partial:'Partial collection receipt',failed:'Collection failed; no successful records reported',unknown:'Outcome unknown after interruption'};
function Receipt({value}){
 if(!value)return null;
 return <div className="card"><strong>{stateText[value.state]}</strong><p>Request <code>{value.request_id}</code> · recipe {value.recipe} · started {new Date(value.started_at).toLocaleString()}</p><p>These are historical accounting receipts, not a coherent current capacity snapshot, host health or execution readiness. Failure or missing results do not prove that nothing changed.</p>
 {value.results.length>0?<ul>{value.results.map(r=><li key={r.binding_sequence}>Native binding {r.binding_sequence}: domain audit {r.domain_sequence??'not reported'}; artifact audit {r.artifact_sequence??'not reported'}</li>)}</ul>:<p>No successful audit sequences are reported in this receipt.</p>}</div>;
}
export function OverheadCollection({read,onRecorded}){
 const [view,setView]=useState(null),[selected,setSelected]=useState(''),[confirmed,setConfirmed]=useState(false),[busy,setBusy]=useState(false),[message,setMessage]=useState(''),[operation,setOperation]=useState(null),[monitoring,setMonitoring]=useState(false);
 const token=useRef(null),active=useRef(null),generation=useRef(0),alive=useRef(true),tracked=useRef(null),refreshed=useRef(new Set());
 useEffect(()=>{alive.current=true;return()=>{alive.current=false;generation.current++;active.current?.abort();if(token.current)token.current.value='';};},[]);
 function clear(){
  generation.current++;active.current?.abort();active.current=null;if(token.current)token.current.value='';
  setBusy(false);setView(null);setSelected('');setConfirmed(false);setMonitoring(false);setOperation(null);tracked.current=null;
  setMessage('This view was cleared. Accepted collection may continue and recorded charges are not undone. Reload durable receipts; no write was retried.');
 }
 async function load(poll=false){
  if(active.current||!read)return;
  const n=++generation.current,control=new AbortController();active.current=control;setBusy(true);setConfirmed(false);
  if(!poll){setView(null);setSelected('');setMessage('');}
  try{
   const next=await read({signal:control.signal});
   if(!alive.current||n!==generation.current)return;
   const trackedOp=tracked.current&&next.operations.find(o=>o.request_id===tracked.current.id);
   if(trackedOp)validateCollectionOperation(trackedOp,tracked.current.choice);
   setView(next);
   if(tracked.current){
    if(!trackedOp){setMonitoring(false);setOperation(null);setMessage('The requested receipt is unavailable. Outcome is unknown; do not infer success from absence.');}
    else{
     setOperation(trackedOp);
     if(trackedOp.state!=='pending'){
      setMonitoring(false);setMessage('Durable receipt loaded. Review its outcome and fresh recipe before approving a separate new collection. No automatic write retry was made.');
      if(!refreshed.current.has(trackedOp.request_id)){refreshed.current.add(trackedOp.request_id);try{await onRecorded?.();}catch{if(alive.current&&n===generation.current)setMessage('Receipt loaded, but the capacity overview could not be refreshed. Recorded charges may have changed.');}}
     }
    }
   }else setMessage('Loaded approved recipes and historical receipts. No host was contacted by this status read.');
  }catch(e){if(alive.current&&n===generation.current){setView(null);setSelected('');setMonitoring(false);setOperation(null);setMessage('Collection status unavailable. Accepted work or partial charges may still exist. Reload durable receipts before any new request.');}}
  finally{if(alive.current&&n===generation.current){active.current=null;setBusy(false);}}
 }
 useEffect(()=>{
  if(!monitoring||busy||!tracked.current)return;
  if(Date.now()-tracked.current.at>180000){setMonitoring(false);setMessage('Automatic status monitoring ended without a terminal receipt. Reload status manually; no collection was retried.');return;}
  const timer=setTimeout(()=>void load(true),2000);return()=>clearTimeout(timer);
 },[monitoring,busy,operation,view]);
 const choice=view?.recipes.find(r=>r.id===selected);
 async function start(){
  if(active.current||!view?.can_start||!choice||!confirmed)return;
  const credential=token.current?.value||'';if(token.current)token.current.value='';
  if(!/^[a-f0-9]{64}$/.test(credential)){setMessage('Enter the separate collection management token. No request was sent.');return;}
  const id=newCollectionRequestID(),n=++generation.current,control=new AbortController();
  tracked.current={id,choice,at:Date.now()};active.current=control;setBusy(true);setView(null);setSelected('');setConfirmed(false);setMessage('Submitting approved accounting collection. This does not authorize activation or remote configuration.');
  const timer=setTimeout(()=>control.abort(),15000);
  try{
   const op=await startCollection(credential,choice,id,{signal:control.signal});
   if(!alive.current||n!==generation.current)return;
   setOperation(op);setMonitoring(op.state==='pending');setMessage(op.state==='pending'?'Collection accepted. Monitoring recorded status only; no write will be retried.':'Historical receipt returned. Reload status before any new request.');
  }catch(e){if(alive.current&&n===generation.current){setMonitoring(false);setOperation(null);setMessage(e.message);}}
  finally{clearTimeout(timer);if(alive.current&&n===generation.current){active.current=null;setBusy(false);}}
 }
 return <details className="card management" onToggle={e=>{if(!e.currentTarget.open)clear();}}><summary>Collect authenticated native overhead</summary>
 <p>Read actual pinned native-owner evidence for an already enrolled server and record conservative domain/artifact ceilings. Charges only increase or remain; this does not install software, reconfigure hosts, free resources, mark health or enable runner creation.</p>
 <button type="button" disabled={busy||!read} onClick={()=>load()}>Load collection recipes and receipts</button>
 {view&&<><p>{view.running?'The last status read reports an active collector operation.':'No collector operation was running at the last status read.'} {view.blocked_reason==='receipt_history_full'?'Receipt history is full; administrator maintenance is required before a new collection.':''}</p>
 <label>Approved collection recipe<select aria-label="Approved collection recipe" value={selected} disabled={busy||!view.can_start} onChange={e=>{setSelected(e.target.value);setConfirmed(false);}}><option value="">Select configured recipe</option>{view.recipes.map(r=><option key={r.id} value={r.id}>{r.id}</option>)}</select></label>
 {choice&&<><ul>{choice.targets.map(t=><li key={t.binding_sequence}>{t.host} · enrolled binding {t.binding_sequence} · collect {t.kind}</li>)}</ul><p>Recipe revision <code>{choice.revision}</code>. Endpoints and credentials are fixed by trusted server configuration, not supplied by this browser.</p>
 <label><input type="checkbox" aria-label="Approve overhead accounting collection" checked={confirmed} disabled={busy||!view.can_start} onChange={e=>setConfirmed(e.target.checked)}/>I approve a new read-and-record operation for these exact enrolled hosts. Conservative charges may increase permanently; this is not cleanup or activation.</label>
 <label>Overhead collection management token<input aria-label="Overhead collection management token" type="password" autoComplete="off" ref={token} disabled={busy}/></label>
 <button type="button" disabled={busy||!view.can_start||!confirmed} onClick={start}>Start new accounting collection</button></>}
 <p>Latest recorded operations (up to 100; first 10 shown). A new explicit collection gets a new request ID; unknown operations are never replayed by this interface.</p>
 {view.operations.slice(0,10).map(op=><details key={op.request_id}><summary>{op.recipe} · {op.state} · {new Date(op.started_at).toLocaleString()}</summary><Receipt value={op}/></details>)}
 {!view.recipes.length&&<p>No approved collection recipes are configured.</p>}</>}
 {operation&&<Receipt value={operation}/>}
 {monitoring&&<p>Reading durable progress periodically. Closing this view stops monitoring, not accepted work.</p>}
 <button type="button" onClick={clear}>{busy?'Cancel request view':'Clear collection view'}</button>
 {message&&<p role="status">{message}</p>}
 </details>;
}
