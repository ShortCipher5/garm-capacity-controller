import React,{useEffect,useRef,useState} from 'react';
import {allocatorGuidance,hasEventTime} from './allocator-status.js';
export function AllocatorStatus({read}){
 const [view,setView]=useState(null),[error,setError]=useState(''),[busy,setBusy]=useState(false),[open,setOpen]=useState(false);
 const active=useRef(null),generation=useRef(0);
 useEffect(()=>()=>{generation.current++;active.current?.abort();},[]);
 function stop(){generation.current++;active.current?.abort();active.current=null;setBusy(false);}
 async function load(){if(!read||active.current)return;const c=new AbortController(),n=++generation.current;active.current=c;setBusy(true);setError('');setView(null);try{const v=await read({signal:c.signal});if(n===generation.current)setView(v.status);}catch{if(n===generation.current)setError('Allocator status unavailable. Previous status was removed; no operation was retried.');}finally{if(n===generation.current){active.current=null;setBusy(false);}}}
 useEffect(()=>{
  if(!open||!read)return;
  const refresh=()=>{if(!document.hidden)void load();else stop();};
  refresh();const timer=setInterval(refresh,15000);document.addEventListener('visibilitychange',refresh);window.addEventListener('focus',refresh);
  return()=>{clearInterval(timer);document.removeEventListener('visibilitychange',refresh);window.removeEventListener('focus',refresh);generation.current++;active.current?.abort();active.current=null;};
 },[open,read]);
 return <section id="allocator-status"><h2>Allocator process status</h2><div className="card"><p>Actual grant-loop activity in this gateway process, separate from the ledger and physical runner lifecycle. No status here authorizes provisioning or proves jobs are running.</p>
 <button disabled={!read} onClick={()=>{stop();setView(null);setError('');setOpen(!open);}}>{open?'Stop watching allocator status':'Watch allocator status'}</button>{open&&<><button disabled={busy} onClick={load}>Refresh allocator status</button><p>Read-only refresh every 15 seconds while this view is open and visible. Closing or hiding pauses reads, not the allocator.</p>{busy&&<p role="status">Reading allocator status…</p>}{error&&<p role="alert">{error}</p>}{view&&<><p role="status">Last reported state: <strong>{view.state}</strong>. {allocatorGuidance(view)}</p><p>Observed {new Date(view.observed_at).toLocaleString()}. Counts are process-local and reset on process restart.</p><p>Attempts: {view.attempt_count}; successful cycles: {view.success_count}; grant count attached to the latest attempt: {view.last_grant_count}.</p>{view.last_batch_id&&<p>Last batch: <code>{view.last_batch_id}</code></p>}<ul>{[['started_at','Service started'],['last_attempt_at','Last cycle attempt'],['last_completed_at','Last cycle finished'],['last_success_at','Last successful cycle'],['stopped_at','Service stopped']].map(([key,label])=><li key={key}>{label}: {hasEventTime(view[key])?new Date(view[key]).toLocaleString():'not recorded'}</li>)}</ul>{view.reason_code&&<p>Reason: <code>{view.reason_code}</code></p>}</>}</>}</div></section>;
}
