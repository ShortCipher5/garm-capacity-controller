import React, {useEffect, useRef, useState} from "react";

const states = {
  missing: "Incus is absent. Install the Debian 13 Incus package and dependencies, then initialize only if the new daemon is pristine.",
  pristine: "Incus is installed but unconfigured. Run its local minimal initialization. This uses directory storage and default local networking, not ZFS.",
  configured: "An existing default profile has a working root storage pool. Preserve this installation; no change is offered.",
  manual_review: "Existing or partial host state was detected. No automatic change is offered. Check the daemon, pool, profile and previous installation before continuing.",
  unsupported: "OS, init system or Incus major version is outside this recipe. No automatic change is offered."
};
export function IncusHostSetup() {
  const [targets, setTargets]=useState([]), [id,setID]=useState(""), [review,setReview]=useState(null), [message,setMessage]=useState(""), [busy,setBusy]=useState(false), [approved,setApproved]=useState(false);
  const token=useRef(null), active=useRef(null);
  useEffect(()=>()=>{active.current?.abort();active.current=null;},[]);
  const target=targets.find(t=>t.target_id===id);
  async function send(action){
    if(active.current || (action!=="list"&&!target) || (action==="apply"&&(!approved||!review?.review_digest)))return;
    const secret=token.current.value.trim();token.current.value="";
    if(!/^[a-f0-9]{64}$/.test(secret)){setMessage("Enter the separate Incus setup token for this action.");return;}
    const controller=new AbortController();active.current=controller;setBusy(true);setApproved(false);setMessage("");
    const timer=setTimeout(()=>controller.abort(),14000);
    const change=action==="apply";
    let body=action==="list"?null:{target_id:id,action,...(change?{review_digest:review.review_digest,confirm_install:true}:{})};
    setReview(null);
    try{
      const response=await fetch("/admin/api/v1/incus-setup",{method:body?"POST":"GET",signal:controller.signal,redirect:"error",cache:"no-store",credentials:"omit",headers:{Authorization:"Bearer "+secret,Accept:"application/json",...(body?{"Content-Type":"application/json"}:{})},...(body?{body:JSON.stringify(body)}:{})});
      const data=await response.json();
      if(active.current!==controller)return;
      if(!response.ok)throw Error(data.error||"Request unavailable.");
      if(action==="list"){if(!Array.isArray(data.targets))throw Error("Invalid target response.");setTargets(data.targets);setMessage("Select a target to inspect. List does not contact hosts.");return;}
      if(data.target_id!==id)throw Error("Unexpected target in response.");
      if(change){if(response.status!==202||data.review_digest!==body.review_digest||data.status!=="unknown")throw Error("Unexpected installation receipt.");setMessage("Installation request recorded. Reload targets and status to check the outcome.");return;}
      if(data.report?.outcome!=="observed"||data.report.placement_eligible!==false||!Object.hasOwn(states,data.report.state))throw Error("Unrecognized host observation.");
      setReview(data);
    }catch(e){if(active.current===controller)setMessage(change?"Outcome unknown. Reload target status, then inspect. Do not replay a lost request.":String(e.message));}
    finally{clearTimeout(timer);if(active.current===controller){active.current=null;setBusy(false);}}
  }
  return <section aria-label="Install or configure Incus"><h4>Install or configure Incus</h4>
    <p>Use a separately approved administrator SSH credential for this step. Supported recipe: Debian 13 with systemd. This does not open a remote API listener, enroll capacity or start runners.</p>
    <label>Incus setup token <input type="password" autoComplete="off" ref={token} disabled={busy}/></label>
    <button type="button" disabled={busy} onClick={()=>send("list")}>Load targets and installation status</button>
    <label>Incus setup target <select value={id} disabled={busy} onChange={e=>{setID(e.target.value);setReview(null);setApproved(false);setMessage("");}}><option value="">Choose a pinned server</option>{targets.map(t=><option key={t.target_id}>{t.target_id}</option>)}</select></label>
    {target&&<><p>Destination: {target.admin_user}@{target.address}:{target.port}. Host pin: <code>{target.host_key}</code>. Administrator key: <code>{target.admin_key_fingerprint}</code>. Recipe: <code>{target.recipe_sha256}</code>.</p>
    <p>Last installation: {target.last_attempt?.status||"none recorded"}{target.running?" (in progress)":""}. An unknown result may include host changes; inspect before retrying and preserve existing data.</p>
    <button type="button" disabled={busy||target.running} onClick={()=>send("inspect")}>Inspect host without installing</button></>}
    {review&&<><p>{states[review.report.state]} {review.report.server_version&&(review.report.state==="missing"?"Debian package candidate: ":"Daemon version: ")+review.report.server_version+"."}</p>
    {review.review_digest&&<><p>Fixed recipe: <code>apt-get install incus=&lt;reviewed Debian package version&gt;</code> only if missing, then <code>incus admin init --minimal</code> only if pristine. No host credentials or custom commands are accepted here. Re-enter the setup token to apply.</p>
    <label><input type="checkbox" checked={approved} onChange={e=>setApproved(e.target.checked)}/> I approve these host changes for the pinned target above.</label>
    <button type="button" disabled={busy||!approved} onClick={()=>send("apply")}>Apply reviewed Incus setup</button></>}</>}
    {message&&<p role="status">{message}</p>}
  </section>
}
