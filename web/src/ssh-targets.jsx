import {NativeAssets} from "./native-assets.jsx";
import {NativeRegistration} from "./native-registration.jsx";
import React,{useEffect,useRef,useState} from 'react';
import {InventoryRotation} from './rotation.jsx';
import {NativePlanning} from './native-planning.jsx';
import {NativePreparation} from './native-preparation.jsx';
import {Firstboot} from './firstboot.jsx';
import {PreparedBootstrap} from './bootstrap.jsx';
import {sshTargetsRequest} from './ssh-targets-client.js';
const empty={id:'',address:'',port:22,user:'',host_key:'',credential_ref:''};
export function SSHTargetRegistration(){
 const [bootstrapEpoch,setBootstrapEpoch]=useState(0);
 const [replaceID,setReplaceID]=useState('');
 const [targets,setTargets]=useState([]),[revokeID,setRevokeID]=useState(''),[confirmRevoke,setConfirmRevoke]=useState(false);
 const [scopes,setScopes]=useState(null),[form,setForm]=useState(empty),[review,setReview]=useState(null),[verified,setVerified]=useState(false),[busy,setBusy]=useState(false),[message,setMessage]=useState('');
 const token=useRef(null),active=useRef(null),pending=useRef(null),generation=useRef(0),mounted=useRef(true);
 useEffect(()=>{mounted.current=true;return()=>{mounted.current=false;generation.current++;active.current?.abort();if(token.current)token.current.value=''}},[]);
 function reset(){setBootstrapEpoch(v=>v+1);const uncertain=pending.current;pending.current=null;generation.current++;active.current?.abort();active.current=null;if(token.current)token.current.value='';setBusy(false);setScopes(null);setTargets([]);setReplaceID('');setRevokeID('');setConfirmRevoke(false);setForm(empty);setReview(null);setVerified(false);setMessage((uncertain==='approve'||uncertain==='replace')?'Approval may already have saved. Reload approved SSH targets before retrying; cancellation does not undo a committed configuration.':uncertain==='revoke'?'Revocation may already have saved. Reload approved SSH targets before retrying; cancellation does not restore permission.':'');}
 function change(key,value){setForm(old=>({...old,[key]:value}));setReview(null);setVerified(false);setMessage('');}
 async function run(action){
  if(action==='approve'&&replaceID)action='replace';
  if(active.current)return;if(action==='revoke'&&(!confirmRevoke||!targets.some(t=>t.id===revokeID)))return;if(['approve','replace'].includes(action)&&(!review||!verified))return;
  const replacing=targets.find(t=>t.id===replaceID);if(action==='replace'&&(!replacing||replaceID===form.id))return;
  const credential=token.current?.value||'';if(token.current)token.current.value='';
  const {id,...config}=form;const revoking=targets.find(t=>t.id===revokeID);const body=action==='scopes'?null:action==='revoke'?{action,target_id:revoking.id,review_digest:revoking.review_digest,confirm_revoke:true,request_id:crypto.randomUUID()}:{action,target_id:id,config,...(['approve','replace'].includes(action)?{...(action==='replace'?{old_target_id:replacing.id,old_review_digest:replacing.review_digest,confirm_replace:true}:{}),review_digest:review.review_digest,verified_host_key:true,request_id:crypto.randomUUID()}:{})};
  const version=++generation.current,controller=new AbortController();active.current=controller;pending.current=action;setBusy(true);setMessage('');setVerified(false);setConfirmRevoke(false);const timeout=setTimeout(()=>controller.abort(),20000);
  try{const value=await sshTargetsRequest(credential,body,{signal:controller.signal});if(!mounted.current||version!==generation.current)return;
   if(action==='scopes'){setScopes(value.scopes);setTargets(value.targets);setReplaceID('');setRevokeID('');setReview(null);setMessage('Choose an approved credential scope, then enter the server and its independently obtained public host key.');}
   else if(action==='revoke'){setReplaceID('');setReview(null);setVerified(false);setTargets(old=>old.filter(t=>t.id!==value.target_id));setRevokeID('');setMessage('Local inventory permission revoked. Already in-flight probes may finish within their timeout. No remote key, running workload or capacity was changed.');}
   else if(action==='review'){setReview(value.target);setMessage('Compare the fingerprint independently, then re-enter the target-management token to save. No connection has been made.');}
   else{setRevokeID('');setConfirmRevoke(false);setTargets(old=>[...old.filter(t=>t.id!==value.target.id&&(action!=='replace'||t.id!==replaceID)),value.target]);setReplaceID('');setReview(null);setMessage(action==='replace'?'Local SSH target replaced atomically. The old ID cannot be reused. Remote keys were not changed; already in-flight probes may finish. No capacity changed.':'SSH target saved. Use Inspect a new server over SSH below to load it and separately authorize a read-only probe. No server was contacted or activated.');}
  }catch(e){if(mounted.current&&version===generation.current){setMessage(e.message);setReview(null)}}
  finally{clearTimeout(timeout);if(mounted.current&&version===generation.current){active.current=null;pending.current=null;setBusy(false)}}
 }
 const scope=scopes?.[form.credential_ref];
 return <details className="card management" id="ssh-target-registration" onToggle={e=>{if(!e.currentTarget.open)reset()}}><summary>Add an SSH server configuration</summary>
 <PreparedBootstrap key={bootstrapEpoch}/><Firstboot key={"firstboot-"+bootstrapEpoch}/><InventoryRotation key={"rotation-"+bootstrapEpoch}/><NativeAssets key={"native-assets-"+bootstrapEpoch}/><NativePreparation key={"native-preparation-"+bootstrapEpoch}/><NativeRegistration key={"native-registration-"+bootstrapEpoch}/><NativePlanning key={"native-planning-"+bootstrapEpoch}/>
 <p>Save a new server within administrator-approved credential and network scopes. This does not install software, contact the server, enroll capacity or upload private keys. Existing target IDs cannot be overwritten.</p>
 <label>SSH target-management token<input aria-label="SSH target-management token" type="password" autoComplete="off" ref={token} disabled={busy}/></label>
 <button type="button" disabled={busy} onClick={()=>run('scopes')}>Load permitted server scopes</button>
 {scopes&&<><label>Replace saved target (optional)<select aria-label="Target to replace" value={replaceID} disabled={busy} onChange={e=>{setReplaceID(e.target.value);setReview(null);setVerified(false)}}><option value="">Add a separate target</option>{targets.map(t=><option key={t.id} value={t.id}>{t.id} · {t.credential_ref}</option>)}</select></label>
 {replaceID&&<p>Replacing {replaceID} permanently withdraws its local permission in the same transaction that saves the new ID. Provision the new key through your trusted administrator first. Remote keys are not changed.</p>}
 <label>Server credential alias<select aria-label="Server credential alias" value={form.credential_ref} disabled={busy} onChange={e=>change('credential_ref',e.target.value)}><option value="">Select credential</option>{Object.keys(scopes).sort().map(alias=><option key={alias}>{alias}</option>)}</select></label>
 {scope&&<p>Allowed networks: {scope.networks.join(', ')} · SSH users: {scope.users.join(', ')} · ports: {scope.ports.join(', ')}</p>}
 <label>New server ID<input aria-label="New server ID" value={form.id} disabled={busy} maxLength={64} onChange={e=>change('id',e.target.value)}/></label>
 <label>Server IP address<input aria-label="Server IP address" value={form.address} disabled={busy} maxLength={45} onChange={e=>change('address',e.target.value)}/></label>
 <label>Server SSH port<input aria-label="Server SSH port" type="number" min={1} max={65535} value={form.port} disabled={busy} onChange={e=>change('port',Number(e.target.value))}/></label>
 <label>Server SSH user<input aria-label="Server SSH user" value={form.user} disabled={busy} maxLength={32} onChange={e=>change('user',e.target.value)}/></label>
 <label>Verified public SSH host key<textarea aria-label="Verified public SSH host key" value={form.host_key} disabled={busy} maxLength={16384} onChange={e=>change('host_key',e.target.value)}/></label>
 <p>Obtain the public host key through an independent trusted channel. Do not paste a private key or accept a key discovered from an unverified connection.</p>
 <button type="button" disabled={busy||!scope} onClick={()=>run('review')}>Review new SSH server</button>
 {review&&<><p>{review.id}: {review.user}@{review.address}:{review.port} · credential {review.credential_ref}</p><code>{review.host_key_fingerprint}</code>
 <label><input aria-label="Approve new SSH server identity" type="checkbox" checked={verified} disabled={busy} onChange={e=>setVerified(e.target.checked)}/>{replaceID?'I approve permanently replacing the selected old target with this new ID. ':''}I independently verified this host-key fingerprint and approve this exact server and credential pairing for future separately requested read-only inventory.</label>
 <button type="button" disabled={busy||!verified||replaceID===form.id} onClick={()=>run('approve')}>{replaceID?'Replace approved SSH server':'Save approved SSH server'}</button></>}
 </>}
 {scopes&&<section><h4>Withdraw an SSH target</h4><p>Revocation prevents subsequent inventory requests. It does not remove a remote SSH key or stop an already in-flight read-only probe. Target IDs remain reserved; adding it again requires a new ID and full approval.</p>
 <label>Target to revoke<select aria-label="Target to revoke" disabled={busy} value={revokeID} onChange={e=>{setRevokeID(e.target.value);setConfirmRevoke(false)}}><option value="">Select saved target</option>{targets.map(t=><option key={t.id} value={t.id}>{t.id} · {t.user}@{t.address}:{t.port} · {t.credential_ref}</option>)}</select></label>
 {revokeID&&<><code>{targets.find(t=>t.id===revokeID)?.host_key_fingerprint}</code><label><input aria-label="Confirm local SSH revocation" type="checkbox" disabled={busy} checked={confirmRevoke} onChange={e=>setConfirmRevoke(e.target.checked)}/>Withdraw this exact saved target's local inventory permission.</label><button type="button" disabled={busy||!confirmRevoke} onClick={()=>run('revoke')}>Revoke local SSH permission</button></>}
 </section>}
 <button type="button" onClick={reset}>{busy?'Cancel server configuration':'Clear server configuration'}</button>
 {message&&<p role="status">{message}</p>}
 </details>;
}
