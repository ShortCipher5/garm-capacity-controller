const hash = value => typeof value === 'string' && /^[a-f0-9]{64}$/.test(value);

// A completed local bundle is not a registry reload, SSH preparation, or host authority.
// Bind the report to a reviewed saved selection instead of trusting echoed identifiers.
export function validateNativeAssemblyReport(value, target, selectionDigest) {
  if (!target || typeof target.target_id !== 'string' || !/^[A-Za-z0-9_-]{1,64}$/.test(target.target_id) || !hash(target.binding?.preparation_id) || !hash(selectionDigest) || value?.target_id !== target.target_id ||
      value.preparation_id !== target.binding?.preparation_id || value.selection_digest !== selectionDigest ||
      ((value.runtime_asset_id !== undefined || value.runtime_asset_manifest_digest !== undefined) && (!hash(value.runtime_asset_id) || !hash(value.runtime_asset_manifest_digest))) ||
      !hash(value.manifest_digest) || !hash(value.plan_digest) || !hash(value.assembly_digest) ||
      value.state !== 'bundle_created_not_reloaded' || value.activation_authorized !== false) {
    throw new Error('Bundle result does not match the reviewed target and saved selection. Inspect the completed assembly; do not automatically build or reload again.');
  }
  return {
    target_id: value.target_id, preparation_id: value.preparation_id,
    selection_digest: value.selection_digest, manifest_digest: value.manifest_digest,
    plan_digest: value.plan_digest, assembly_digest: value.assembly_digest,
    ...(value.runtime_asset_id === undefined ? {} : {runtime_asset_id:value.runtime_asset_id,runtime_asset_manifest_digest:value.runtime_asset_manifest_digest}),
    state: value.state, activation_authorized: false,
  };
}

export function validateNativeRecoveryStatus(value,target,selectionDigest) {
  if(!hash(target?.binding?.preparation_id)||typeof target?.target_id!=='string'||!value||value?.target_id!==target?.target_id||value?.preparation_id!==target?.binding?.preparation_id||value?.selection_digest!==selectionDigest||!hash(selectionDigest)||value?.activation_authorized!==false||typeof value?.partial_output_present!=='boolean'||!['never_attempted','assembly_unknown','completed_bundle'].includes(value?.state))throw Error('Local recovery response does not match the selected configuration. Status remains unavailable; no retry is authorized.');
  if(value.state==='completed_bundle'){
    if(value.partial_output_present)throw Error('Inconsistent completed bundle status. Load local recovery status again.');
    return {target_id:value.target_id,preparation_id:value.preparation_id,selection_digest:value.selection_digest,state:value.state,partial_output_present:false,activation_authorized:false,assembly:validateNativeAssemblyReport(value.assembly,target,selectionDigest)};
  }
  if(value.assembly!==undefined||(value.state==='never_attempted'&&value.partial_output_present))throw Error('Inconsistent local recovery status. Do not infer that no attempt occurred.');
  return {target_id:value.target_id,preparation_id:value.preparation_id,selection_digest:value.selection_digest,state:value.state,partial_output_present:value.partial_output_present,activation_authorized:false};
}

import {validatePreparationView} from './native-preparation-client.js';
async function expectedHostFingerprint(target) {
  try {
    const parts=target.binding.native_target.host_key.trim().split(/\s+/);
    if(parts[0]!=='ssh-ed25519'||!parts[1])throw Error();
    const wire=Uint8Array.from(atob(parts[1]),c=>c.charCodeAt(0));
    const digest=new Uint8Array(await crypto.subtle.digest('SHA-256',wire));
    return 'SHA256:'+btoa(String.fromCharCode(...digest)).replace(/=+$/,'');
  } catch { throw Error('Pinned native target identity is unavailable. Reload the planning target.'); }
}
async function matchPreparation(view,target) {
  const n=target.binding.native_target;
  if(view.target_id!==target.target_id||view.preparation_id!==target.binding.preparation_id||view.address!==n.address||view.port!==n.port||view.admin_user!==n.admin_user||view.admin_key_fingerprint!==n.admin_key_fingerprint||view.host_key_fingerprint!==await expectedHostFingerprint(target))throw Error('Preparation registry does not match the exact saved target binding. No action is authorized.');
  return view;
}
export const isPending=v=>v?.state==='registered_configuration_without_preparation_plan';
function matchPending(v,target){if(!isPending(v)||v.target_id!==target.target_id||!hash(v.registration_digest)||JSON.stringify(v.binding)!==JSON.stringify(target.binding))throw Error('Pending registration does not match the exact saved server.');return {target_id:v.target_id,registration_digest:v.registration_digest,binding:v.binding,state:v.state};}
export async function nativeAssemblyRequest(token,action,target,selection,current,assembly,{fetcher=fetch,signal}={}) {
  if(!hash(token))throw Error('Enter the separate native preparation token.');
  if(!target||selection?.state!=='saved_configuration_not_applied'||selection?.selection?.physical_capacity_qualified!==false||JSON.stringify(selection?.selection?.binding)!==JSON.stringify(target.binding)||!hash(selection?.selection_digest))throw Error('Select an actual saved configuration for this target.');
  const selectionDigest=selection.selection_digest;
  let body=null;
  if(action){
    if(!['build','inspect','reload','initial_plan','status'].includes(action))throw Error('Unsupported assembly action.');
    body={action,target_id:target.target_id,selection_digest:selectionDigest};
    if(action==='build'){
      if(!current||(!isPending(current)&&current.state?.status!=='not_started')||!Number.isFinite(Date.parse(selection.selection.expires_at))||Date.parse(selection.selection.expires_at)<=Date.now()||!Number.isFinite(Date.parse(selection.selection.saved_at))||Date.parse(selection.selection.saved_at)>Date.now())throw Error('A fresh saved selection and unconsumed registered preparation are required.');
      if(isPending(current))matchPending(current,target);else await matchPreparation(validatePreparationView({targets:[current]}).targets[0],target);
      body.confirm_build=true;
    }
    if(action==='initial_plan'){
      const reviewed=validateNativeAssemblyReport(assembly,target,selectionDigest);matchPending(current,target);body={...body,assembly_digest:reviewed.assembly_digest,registration_digest:current.registration_digest,confirm_initial_plan:true};
    }
    if(action==='reload'){
      const reviewed=validateNativeAssemblyReport(assembly,target,selectionDigest);
      await matchPreparation(validatePreparationView({targets:[current]}).targets[0],target);
      if(current.state.status!=='not_started'||current.review_digest===reviewed.plan_digest)throw Error('Load the actual unconsumed registry and review the changed bundle before reload.');
      body={...body,assembly_digest:reviewed.assembly_digest,current_review_digest:current.review_digest,confirm_reload:true};
    }
  }
  let response;
  try { response=await fetcher(action==='initial_plan'?'/admin/api/v1/native-registration':'/admin/api/v1/native-assembly',{method:body?'POST':'GET',headers:{Authorization:`Bearer ${token}`,Accept:'application/json',...(body?{'Content-Type':'application/json'}:{})},...(body?{body:JSON.stringify(body)}:{}),credentials:'omit',redirect:'error',cache:'no-store',signal}); }
  catch { throw Error(action==='build'?'Bundle assembly outcome uncertain. Inspect this exact selection; never automatically rebuild partial output.':(action==='reload'||action==='initial_plan')?'Registry approval outcome uncertain. Reload the current registry before any further action; no SSH preparation was requested.':'Assembly read unavailable. Clear the review and load again.'); }
  if(response.status!==200)throw Error(response.status===401?'Native preparation token rejected.':response.status===404?'Native assembly is not configured for this deployment.':action==='build'?'Assembly rejected or uncertain. Inspect the exact selection, not an automatic rebuild.':(action==='reload'||action==='initial_plan')?'Registry approval rejected or uncertain. Load the current registry; consumed preparation IDs cannot be replaced.':'Assembly status unavailable. No completion or permission can be inferred.');
  let value;try{value=await response.json();}catch{throw Error('Assembly response unreadable. Outcome may be uncertain; inspect the exact assembly and load the current registry rather than repeating a write.');}
  if(!value||typeof value!=='object'||Array.isArray(value)||signal?.aborted)throw Error('Assembly result unavailable or outcome uncertain. Read current status before any further action.');
  if(!action){const found=validatePreparationView(value).targets.find(v=>v.target_id===target.target_id);const rows=value.pending_targets||[];if(!Array.isArray(rows)||rows.length>8||new Set(rows.map(r=>r?.target_id)).size!==rows.length)throw Error('Pending configuration unavailable.');const pending=rows.find(v=>v?.target_id===target.target_id);if(found&&pending)throw Error('Ambiguous pending and prepared configuration.');if(pending)return matchPending(pending,target);if(!found)throw Error('This target has no configured assembly recipe.');return matchPreparation(found,target);}
  if(value.target_id!==target.target_id)throw Error('Assembly response target mismatched; outcome uncertain.');
  if(action==='status')return validateNativeRecoveryStatus(value.recovery,target,selectionDigest);
  const report=validateNativeAssemblyReport(value.assembly,target,selectionDigest);
  if(action!=='reload'&&action!=='initial_plan')return report;
  const view=await matchPreparation(validatePreparationView({targets:[value.preparation]}).targets[0],target);
  if(value.state!=='registry_reloaded_not_prepared'||view.review_digest!==report.plan_digest||view.manifest_digest!==report.manifest_digest||view.state.status!=='not_started'||report.assembly_digest!==assembly.assembly_digest||report.plan_digest!==assembly.plan_digest||report.manifest_digest!==assembly.manifest_digest||report.runtime_asset_id!==assembly.runtime_asset_id||report.runtime_asset_manifest_digest!==assembly.runtime_asset_manifest_digest)throw Error('Registry response does not match the exact reviewed bundle. Reload current status; do not request SSH preparation from this response.');
  return {assembly:report,preparation:view,state:value.state};
}
