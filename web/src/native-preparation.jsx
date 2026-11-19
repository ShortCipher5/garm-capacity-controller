import React, {useEffect, useRef, useState} from 'react';
import {nativePreparationRequest,preparedDiagnosticRequest} from './native-preparation-client.js';
export function NativePreparation() {
  const [targets,setTargets]=useState([]),[selected,setSelected]=useState(''),[approved,setApproved]=useState(false),[busy,setBusy]=useState(false),[message,setMessage]=useState('');
  const [accepted,setAccepted]=useState(null),[diagnosticApproved,setDiagnosticApproved]=useState(false),[report,setReport]=useState(null);
  const token=useRef(null),active=useRef(null),generation=useRef(0),mounted=useRef(true);
  useEffect(()=>{mounted.current=true;return()=>{mounted.current=false;generation.current++;active.current?.abort();if(token.current)token.current.value='';};},[]);
  const target=targets.find(t=>t.target_id===selected), inspect=target?.state.status!=='not_started';
  function clear(){setDiagnosticApproved(false);setReport(null);setAccepted(null);generation.current++;active.current?.abort();active.current=null;if(token.current)token.current.value='';setTargets([]);setSelected('');setApproved(false);setBusy(false);setMessage('Cleared local review. An accepted preparation may continue; reload durable status. Never repeat an uncertain upload.');}
  async function run(action){
    const diagnostic=action==='diagnostic';
    if(active.current||(diagnostic?(!target?.diagnostic||!diagnosticApproved):(action&&(!target||!approved||target.state.attempt?.sequence===1024))))return;
    setReport(null);setDiagnosticApproved(false);
    if(action)setAccepted(null);
    const credential=token.current?.value||'';if(token.current)token.current.value='';
    const version=++generation.current,controller=new AbortController();active.current=controller;setBusy(true);setApproved(false);setMessage('');const timeout=setTimeout(()=>controller.abort(),diagnostic?45000:20000);
    try {const result=diagnostic?await preparedDiagnosticRequest(credential,target,{signal:controller.signal}):await nativePreparationRequest(credential,action?target:null,action?inspect:false,{signal:controller.signal});if(!mounted.current||version!==generation.current)return;
      if(diagnostic){setReport(result);setMessage('Read-only diagnostic returned. This point-in-time report remains unqualified; no preparation history or activation was changed.');return;}
      setSelected('');if(action){if(result.state.attempt)setAccepted({target_id:target.target_id,preparation_id:target.preparation_id,review_digest:result.state.attempt.review_digest,sequence:result.state.attempt.sequence,mode:result.state.attempt.mode});setTargets([]);setMessage(inspect?'Inspection accepted with unknown outcome. Reload historical status; an older preparation receipt does not prove this inspection succeeded. No files are re-uploaded.':'The exact request was durably recorded with unknown outcome. Re-enter this dedicated token and reload status. An accepted request may continue after closing this view. Inspection never re-uploads files.');}else{setTargets(result.targets);setMessage(result.targets.length?'Select a deployment-approved native preparation target. Loading status does not contact the server.':'No native preparation targets are configured.');}
    }catch(e){if(mounted.current&&version===generation.current){setTargets([]);setSelected('');setMessage(e.message);}}
    finally{clearTimeout(timeout);if(mounted.current&&version===generation.current){active.current=null;setBusy(false);}}
  }
  return <section aria-label="Native owner preparation"><h4>Prepare a native owner on an approved server</h4>
    <p>Transfers a fixed deployment-approved bundle, including private owner configuration and TLS material, to its pinned administrator SSH destination. Preparation stages files only. It does not activate the owner, enroll capacity, start runners or prove production readiness. This is separate from inventory-listener installation.</p>
    <label>Separate native preparation token<input aria-label="Separate native preparation token" type="password" autoComplete="off" ref={token} disabled={busy}/></label>
    <button type="button" disabled={busy} onClick={()=>run(false)}>Load native preparation targets and status</button>
    <label>Native preparation target<select aria-label="Native preparation target" disabled={busy} value={selected} onChange={e=>{setSelected(e.target.value);setApproved(false);setDiagnosticApproved(false);setReport(null);setMessage('');}}><option value="">Select approved native target</option>{targets.map(t=><option key={t.target_id} value={t.target_id}>{t.target_id}</option>)}</select></label>
    {target&&<><p>Destination: {target.admin_user}@{target.address}:{target.port} · Bundle: {target.bundle_bytes.toLocaleString()} bytes.</p><p>Verify pinned SSH host key <code>{target.host_key_fingerprint}</code> and administrator credential key <code>{target.admin_key_fingerprint}</code> against the deployment approval.</p><p>Bundle manifest <code>{target.manifest_digest}</code>; fixed upload script <code>{target.script_sha256}</code>.</p><p>Permanent preparation ID <code>{target.preparation_id}</code>; reviewed action <code>{target.review_digest}</code>.</p>
      <p>Durable status: {target.state.status.replaceAll('_',' ')}. This is recorded history, not a running-process indicator. Unknown does not mean failed or rolled back.</p>
      {target.state.receipt&&<p>Historical preparation receipt: manifest <code>{target.state.receipt.manifest_digest}</code>; unit <code>{target.state.receipt.unit_digest}</code>. This is not current health, activation or capacity evidence.</p>}
      {target.diagnostic&&<div aria-label="Prepared bundle diagnostic review"><p>Read the fixed staged diagnostic for this exact prepared bundle. Artifact <code>{target.diagnostic.artifact_sha256}</code>; read script <code>{target.diagnostic.script_sha256}</code>; diagnostic review <code>{target.diagnostic.review_digest}</code>. No upload, installation, activation or capacity qualification.</p><label><input aria-label="Approve prepared bundle diagnostic read" type="checkbox" checked={diagnosticApproved} disabled={busy} onChange={e=>setDiagnosticApproved(e.target.checked)}/>Run this fixed read-only diagnostic on the pinned server. Results remain unqualified.</label><button type="button" disabled={busy||!diagnosticApproved} onClick={()=>run('diagnostic')}>Read prepared bundle diagnostics</button></div>}
      {report&&<div aria-label="Prepared bundle diagnostic result"><h5>Unqualified diagnostic observation</h5><p>Observed {report.observed_at}. Historical observation only, not current authority, readiness or activation permission.</p><ul>{report.checks.map(c=><li key={c.name}>{c.name}: {c.state.replaceAll('_',' ')}{c.observed?` · observed: ${c.observed}`:''} · required: {c.required}</li>)}</ul></div>}
      <PreparationAttempt target={target} accepted={accepted}/>{target.state.attempt?.sequence===1024&&<p role="alert">Preparation attempt history is full. No further preparation or inspection attempts are available; do not reset history or repeat the upload.</p>}
      <label><input aria-label="Approve exact native preparation action" type="checkbox" checked={approved} disabled={busy||target.state.attempt?.sequence===1024} onChange={e=>setApproved(e.target.checked)}/>{inspect?'I approve inspecting this same preparation ID on the pinned server. Do not upload again, restart, activate or adopt partial files.':'I approve this exact pinned destination and deployment bundle, including transfer of private owner configuration and TLS material. Stage files only; do not activate.'}</label>
      <button type="button" disabled={busy||!approved||target.state.attempt?.sequence===1024} onClick={()=>run(true)}>{inspect?'Inspect same native preparation only':'Prepare reviewed native owner bundle'}</button></>}
    <button type="button" onClick={clear}>{busy?'Close native preparation request view':'Clear native preparation review'}</button>{message&&<p role="status">{message}</p>}
  </section>;
}

function PreparationAttempt({target,accepted}) {
  const attempt=target.state.attempt;
  if(!attempt)return <p>Latest attempt details are unavailable. A historical preparation receipt does not prove a later inspection succeeded.</p>;
  const matches=accepted&&accepted.target_id===target.target_id&&accepted.preparation_id===target.preparation_id&&accepted.review_digest===attempt.review_digest&&accepted.sequence===attempt.sequence&&accepted.mode===attempt.mode;
  return <div aria-label="Latest native preparation attempt"><h5>Latest recorded {attempt.mode} attempt · {attempt.sequence}</h5>
    <p>Started {attempt.started_at}{attempt.completed_at?`; finished ${attempt.completed_at}`:''}. Recorded outcome: {attempt.status.replaceAll('_',' ')}.</p>
    <p>{attempt.status==='unknown'?'Outcome unknown: this record does not establish whether the process is still running. Do not repeat the upload.':attempt.status==='observation_unavailable'?'This attempt did not confirm preparation. It may have had effects; an older receipt is not evidence this attempt succeeded.':'The recorded attempt verified prepared files. This is point-in-time preparation evidence, not activation, continuous health or permission to run workloads.'}</p>
    <p>{matches?'This record matches the exact attempt accepted in this view.':'This is the latest recorded attempt, not confirmation of a particular request from this view.'}</p>
  </div>;
}
