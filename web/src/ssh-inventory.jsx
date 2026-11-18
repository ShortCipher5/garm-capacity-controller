import React,{useEffect,useRef,useState} from 'react';
import {sshInventoryRequest,validateSSHToken} from './ssh-inventory-client.js';
export function SSHInventory(){
 const [view,setView]=useState(null),[selected,setSelected]=useState(''),[verified,setVerified]=useState(false),[busy,setBusy]=useState(false),[message,setMessage]=useState(''),[result,setResult]=useState(null);
 const token=useRef(null),active=useRef(null),generation=useRef(0),mounted=useRef(true);
 useEffect(()=>{mounted.current=true;return()=>{mounted.current=false;generation.current++;active.current?.abort();if(token.current)token.current.value=''}},[]);
 function reset(){generation.current++;active.current?.abort();active.current=null;if(token.current)token.current.value='';setBusy(false);setView(null);setSelected('');setVerified(false);setResult(null);setMessage('');}
 async function run(probe){
  if(active.current)return;
  const credential=token.current?.value||'';try{validateSSHToken(credential)}catch(e){setMessage(e.message);return}
  const target=view?.targets.find(t=>t.id===selected);if(probe&&(!target||!verified)){setMessage('Select and verify a configured target first.');return}
  const body=probe?{target_id:target.id,review_digest:target.review_digest,verified_host_key:true,request_id:crypto.randomUUID()}:null;
  const version=++generation.current;const controller=new AbortController();active.current=controller;if(token.current)token.current.value='';setBusy(true);setMessage('');setResult(null);setVerified(false);
  const timeout=setTimeout(()=>controller.abort(),20000);
  try{const value=await sshInventoryRequest(credential,body,{signal:controller.signal});if(!mounted.current||generation.current!==version)return;
   if(probe){setResult(value);setMessage('Read-only inventory complete. This server has not been enrolled or activated.');}
   else{setView(value);setSelected(value.targets[0]?.id||'');setMessage('Review the exact destination, credential alias and independently verified host key before probing.');}
  }catch(e){if(mounted.current&&generation.current===version){setMessage(e.message);setResult(null);setVerified(false);}}
  finally{clearTimeout(timeout);if(mounted.current&&generation.current===version){active.current=null;setBusy(false);}}
 }
 const target=view?.targets.find(t=>t.id===selected);const inventory=result?.inventory;
 const dimension=(v,unit)=>v===undefined?`unknown ${unit}`:`${v.toLocaleString()} ${unit}`;
 return <details className="card management" id="ssh-inventory" onToggle={e=>{if(!e.currentTarget.open)reset()}}><summary>Inspect a new server over SSH</summary>
  <p>Staged read-only discovery using a deployment-authorized address, SSH host key and credential pairing. No private keys or passwords are entered here. No agent is installed and no server is activated.</p>
  <label>SSH inventory token<input aria-label="SSH inventory token" type="password" autoComplete="off" ref={token} disabled={busy}/></label>
  <button type="button" disabled={busy} onClick={()=>run(false)}>Load approved SSH targets</button>
  {view&&<><label>SSH target<select aria-label="SSH target" value={selected} disabled={busy} onChange={e=>{setSelected(e.target.value);setVerified(false);setResult(null);setMessage('')}}>{view.targets.map(t=><option key={t.id} value={t.id}>{t.id} · {t.address}:{t.port}</option>)}</select></label>
   {!view.targets.length&&<p>No deployment-approved SSH destinations are configured.</p>}
   {target&&<div><p>{target.user}@{target.address}:{target.port} · credential alias {target.credential_ref}</p><code>{target.host_key_fingerprint}</code>
    <label><input aria-label="Verified SSH host key" type="checkbox" checked={verified} disabled={busy} onChange={e=>setVerified(e.target.checked)}/>I independently verified this SSH host-key fingerprint and authorize this exact destination and credential pairing for read-only inventory.</label>
    <p>The token was cleared after loading. Re-enter it to run the reviewed probe.</p>
    <button type="button" disabled={busy||!verified} onClick={()=>run(true)}>Run reviewed SSH inventory</button>
   </div>}
  </>}
  <button type="button" onClick={reset}>{busy?'Cancel SSH inventory':'Clear SSH review'}</button>
  {message&&<p role="status">{message}</p>}
  {inventory&&<div><h4>Authenticated OS observation</h4><p>Observed {new Date(inventory.observed_at).toLocaleString()} · {inventory.complete?'all supported dimensions observed':'some dimensions unknown'}</p>
   <p>Logical CPU: {dimension(inventory.logical_cpus,'CPUs')} · Memory: {dimension(inventory.ram_mib,'MiB')} · Currently available memory: {dimension(inventory.available_ram_mib,'MiB')}</p>
   <p>Root filesystem: {dimension(inventory.root_disk_mib,'MiB')} · Currently available: {dimension(inventory.root_available_mib,'MiB')}</p>
   <p className="drain-warning">This may be a VM/container OS view. It is not a physical-capacity guarantee, Incus storage inventory, saved runner budget or permission to start runners.</p>
  </div>}
 </details>;
}
