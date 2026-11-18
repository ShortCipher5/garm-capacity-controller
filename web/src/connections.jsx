import React,{useEffect,useRef,useState} from 'react';
import {connectionRequest,validateConnectionToken} from './connections-client.js';
export function ConnectionManager({onChanged}){
 const [view,setView]=useState(null),[draft,setDraft]=useState(null),[selected,setSelected]=useState(null),[verified,setVerified]=useState(false),[busy,setBusy]=useState(false),[blocked,setBlocked]=useState(false),[message,setMessage]=useState('');
 const token=useRef(null),active=useRef(null),mounted=useRef(true);
 useEffect(()=>()=>{mounted.current=false;active.current?.abort();if(token.current)token.current.value=''},[]);
 async function run(body){
  if(active.current||body&&blocked)return;
  const credential=token.current?.value||'';
  try{validateConnectionToken(credential)}catch(error){setMessage(error.message);return}
  const controller=new AbortController();active.current=controller;setBusy(true);setMessage('');const timeout=setTimeout(()=>controller.abort(),15000);
  try{
   const result=await connectionRequest(credential,body,{signal:controller.signal});if(!mounted.current)return;
   if(body){setView(previous=>({...previous,connections:[...previous.connections.filter(c=>c.id!==result.id),result]}));setDraft(null);setSelected(null);setVerified(false);onChanged?.();setMessage('Connection intent updated. Approval permits only explicit discovery, not host activation.');}
   else{setView(result);setBlocked(false);setDraft(null);setSelected(null);setVerified(false);setMessage('Loaded current connection revisions.');}
  }catch(error){if(mounted.current){setMessage(error.message);if(error.unknown)setBlocked(true)}}
  finally{clearTimeout(timeout);active.current=null;if(token.current)token.current.value='';if(mounted.current)setBusy(false)}
 }
 function save(event){event.preventDefault();const target=view.targets.find(t=>t.credential_ref===draft.credential_ref);if(!target){setMessage('Choose a deployment-approved destination.');return}if(!/^[A-Za-z0-9_-]{1,64}$/.test(draft.id)||!/^[0-9a-f]{64}$/.test(draft.server_key_sha256)){setMessage('Use a valid profile ID and a 64-character lowercase public-key fingerprint.');return}
  run({action:'save-draft',expected_revision:draft.revision||0,connection:{id:draft.id,credential_ref:target.credential_ref,endpoint:target.endpoint,project:target.project,server_key_sha256:draft.server_key_sha256}});
 }
 return <details className="card management" id="connection-management"><summary>Add or manage server connections</summary>
  <p>Use a deployment-approved Incus destination and existing credential alias. No passwords or private keys are entered here. Saving a draft does not connect; approving an independently verified key permits explicit read-only discovery only.</p>
  <label>Connection management token<input aria-label="Connection management token" ref={token} type="password" autoComplete="off" disabled={busy}/></label>
  <button type="button" disabled={busy} onClick={()=>run(null)}>Reload connections</button>
  {blocked&&<p className="drain-warning">Writes paused until connection state is reloaded. The last operation may have taken effect.</p>}
  {view&&<>
   <p>{view.targets.length} deployment-approved destinations. Missing destination? The deployment operator must authorize its exact IP, port, project and credential alias first.</p>
   <button type="button" disabled={busy||blocked||!view.targets.length} onClick={()=>{setDraft({id:'',revision:0,credential_ref:view.targets[0].credential_ref,server_key_sha256:view.targets[0].expected_key||''});setSelected(null);setVerified(false)}}>Add connection draft</button>
   <ul>{view.connections.map(c=><li key={c.id}><strong>{c.id}</strong> · {c.state} · revision {c.revision} · {c.endpoint} · project {c.project}
    <button type="button" disabled={busy||blocked} onClick={()=>{setDraft(c);setSelected(null);setVerified(false)}}>Edit {c.id}</button>
    {c.state==='draft'&&<button type="button" disabled={busy||blocked} onClick={()=>{setSelected(c);setDraft(null);setVerified(false)}}>Review key for {c.id}</button>}
    {c.state!=='revoked'&&<button type="button" disabled={busy||blocked} onClick={()=>run({action:'revoke',id:c.id,expected_revision:c.revision})}>Revoke local connection {c.id}</button>}
   </li>)}</ul>
   <p>Local revocation blocks further controller discovery and invalidates old reviews. It does not remove the client certificate from Incus or stop existing runners.</p>
  </>}
  {draft&&<form onSubmit={save}>
   <label>Connection profile ID<input aria-label="Connection profile ID" value={draft.id} disabled={busy||!!draft.revision} onChange={e=>setDraft({...draft,id:e.target.value})} required/></label>
   <label>Approved destination<select aria-label="Approved destination" value={draft.credential_ref} disabled={busy} onChange={e=>{const target=view.targets.find(t=>t.credential_ref===e.target.value);setDraft({...draft,credential_ref:target.credential_ref,server_key_sha256:target.expected_key||''})}}>{view.targets.map(t=><option key={t.credential_ref} value={t.credential_ref}>{t.endpoint} / {t.project} ({t.credential_ref})</option>)}</select></label>
   <label>Expected server public-key SHA-256<input aria-label="Expected server public-key SHA-256" value={draft.server_key_sha256} disabled={busy} onChange={e=>setDraft({...draft,server_key_sha256:e.target.value})} required maxLength={64}/></label>
   <p>Use the independently verified SPKI fingerprint, not the full certificate fingerprint or a key accepted from an unverified connection. Every edit resets approval and invalidates older discovery reviews.</p>
   <button type="submit" disabled={busy||blocked}>Save connection draft</button><button type="button" disabled={busy} onClick={()=>setDraft(null)}>Cancel draft</button>
  </form>}
  {selected&&<div><h4>Review connection trust</h4><p>{selected.id} · {selected.endpoint} · {selected.project} · revision {selected.revision}</p><code>{selected.server_key_sha256}</code>
   <label><input type="checkbox" checked={verified} disabled={busy} onChange={e=>setVerified(e.target.checked)}/>I verified this public-key fingerprint through an independent trusted channel.</label>
   <button type="button" disabled={!verified||busy||blocked} onClick={()=>run({action:'approve',id:selected.id,expected_revision:selected.revision,approved_key:selected.server_key_sha256})}>Approve reviewed connection key</button>
   <button type="button" disabled={busy} onClick={()=>{setSelected(null);setVerified(false)}}>Cancel review</button>
  </div>}
  {message&&<p role="status">{message}</p>}
 </details>;
}
