import React, {useState,useRef,useEffect} from 'react';
import {registerPoolMapping,validMappingSize} from './broker-profiles-client.js';
export function PoolMappings({readMappings,readProfiles}) {
 const [view,setView]=useState(null),[sizes,setSizes]=useState([]),[recipe,setRecipe]=useState(''),[size,setSize]=useState(''),[review,setReview]=useState(null),[busy,setBusy]=useState(false),[blocked,setBlocked]=useState(true),[message,setMessage]=useState('');
 const active=useRef(null),alive=useRef(true),token=useRef(null);
 useEffect(()=>{alive.current=true;return()=>{alive.current=false;active.current?.abort();if(token.current)token.current.value='';};},[]);
 async function load(){
  if(active.current||!readMappings||!readProfiles)return;
  const c=new AbortController();active.current=c;setBusy(true);setBlocked(true);setReview(null);setMessage('');setView(null);setSizes([]);
  try {const [v,s]=await Promise.all([readMappings(),readProfiles()]);if(alive.current&&!c.signal.aborted){setView(v);setSizes(s.profiles);setRecipe('');setSize('');setBlocked(false);}}
  catch {if(alive.current)setMessage('Could not load current mappings and sizes. Reload before editing.');}
  finally {active.current=null;if(alive.current)setBusy(false);}
 }
 async function save(e){
  e.preventDefault();if(active.current||!review||blocked)return;
  const secret=token.current?.value||'';if(token.current)token.current.value='';
  if(!/^[a-f0-9]{64}$/.test(secret)){setMessage('Enter the separate 64-character management token.');return;}
  const c=new AbortController();active.current=c;setBusy(true);setBlocked(true);const timer=setTimeout(()=>c.abort(),15000);
  try {await registerPoolMapping(secret,review,{signal:c.signal});if(alive.current)setMessage('Immutable mapping recorded. No runners, grants or pool settings changed. Reload before another edit.');}
  catch(error){if(alive.current)setMessage(error.message);}
  finally{clearTimeout(timer);active.current=null;if(alive.current){setBusy(false);setReview(null);}}
 }
 const selected=sizes.find(p=>p.name===size),choice=view?.recipes.find(r=>r.id===recipe);
 function prepare(){if(!selected||!choice)return;const resources={cpu_millis:selected.cpu_millis,ram_mib:selected.ram_mib,disk_mib:selected.disk_mib,runners:1};if(!validMappingSize(resources))return;setReview({recipe,recipe_revision:choice.revision,pool:choice.pool,size_profile:size,expected_sequence:view.profiles.find(p=>p.pool===choice.pool)?.sequence||0,resources});setMessage('');}
 return <div className="card"><h3>Pool runner-size mappings</h3>
 <p>Bind a saved runner size to a deployment-configured pool template and enrolled native hosts. This records configuration only, not an active scheduler or permission to provision.</p>
 <button type="button" disabled={busy||!readMappings} onClick={load}>Load mappings and saved sizes</button>
 {view&&<><ul>{view.profiles.map(p=><li key={p.pool}>Pool {p.pool}: {p.size_profile}, {p.resources.cpu_millis/1000} vCPU / {p.resources.ram_mib} MiB RAM / {p.resources.disk_mib} MiB disk. Hosts: {p.hosts.join(', ')}. Revision {p.sequence}.</li>)}</ul>
 {!view.profiles.length&&<p>No immutable pool mappings recorded.</p>}
 {!view.writes_enabled&&<p>Registration unavailable: an operator must configure private registration recipes and enable separate management access. No browser endpoint or credential upload is accepted.</p>}
 <fieldset disabled={busy||blocked||!view.writes_enabled||!!review}>
 <label>Configured pool template<select value={recipe} onChange={e=>setRecipe(e.target.value)}><option value="">Choose template</option>{view.recipes.map(r=><option key={r.id} value={r.id}>{r.id} · {r.pool}</option>)}</select></label>
 <label>Saved runner size<select value={size} onChange={e=>setSize(e.target.value)}><option value="">Choose size</option>{sizes.filter(p=>p.disk_mib>0).map(p=><option key={p.name} value={p.name}>{p.name} · {p.cpu_millis/1000} vCPU / {p.ram_mib} MiB RAM / {p.disk_mib} MiB disk</option>)}</select></label>
 <p>Mapping requires positive bounded disk. Editing or deleting a saved size does not alter existing frozen mappings. Register a new revision to adopt a changed size.</p>
 <button type="button" disabled={!selected||!choice} onClick={prepare}>Review mapping</button></fieldset>
 {review&&<form onSubmit={save}><p>Record {review.size_profile} for template {review.recipe}: {review.resources.cpu_millis/1000} vCPU, {review.resources.ram_mib} MiB RAM, {review.resources.disk_mib} MiB disk. Replace revision {review.expected_sequence}. Fresh stock ownership and exact native policy must validate; existing charges stay unchanged.</p><label>Separate management token<input ref={token} type="password" autoComplete="off" disabled={busy}/></label><button type="submit" disabled={busy}>Record mapping</button><button type="button" disabled={busy} onClick={()=>{if(token.current)token.current.value='';setReview(null);}}>Cancel</button></form>}
 </>}{message&&<p role="status">{message}</p>}</div>;
}
