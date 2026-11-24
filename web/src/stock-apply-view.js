// Read-only renderer contract for the durable stock-apply controller view.
// This module never enables a control and never implies a production
// permission grant: malformed, untrusted or unsupported payloads collapse to
// a conservative unavailable view.
const count=v=>Number.isSafeInteger(v)&&v>=0&&v<=100000;
const scope=v=>typeof v==='string'&&/^[0-9a-f]{64}$/.test(v);
const reason=v=>typeof v==='string'&&/^[a-z0-9_]{0,96}$/.test(v);
const text=v=>typeof v==='string'&&v.length>0&&v.length<=512;
const exact=(v,keys)=>v&&typeof v==='object'&&!Array.isArray(v)&&Object.keys(v).length===keys.length&&keys.every(k=>Object.hasOwn(v,k));
const states=new Set(['unavailable','proposed','paused_maintenance','applied_when_proven','outcome_unknown']);
const actionNames=new Set(['apply','withdraw_proposal','pause','resume','resolve_outcome','rollback']);
const alwaysDisabled=new Set(['apply','resolve_outcome','rollback']);
const refusalCodes=new Set(['interface_unsupported','stock_garm_v0_2_1_observe_only','external_ownership_unproven','completion_proof_unavailable','late_writer_fence_unproven','requirement_unestablished','physical_allocation_teardown_out_of_scope']);
const operationStates=new Set(['proposed','dispatched','outcome_unknown','applied_when_proven','withdrawn']);
const operationKeys=['operation_id','scope','pool_id','repository_id','source','fingerprint','observed','suggested','charged','direction','state','drift','plan_expires_at','created_at','updated_at'];
const timestamp=v=>typeof v==='string'&&/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$/.test(v);

function unavailableView(code='view_unavailable'){
 return {state:'unavailable',reason:code,paused:null,drift:null,operation:null,
  charged:null,
  capability:{supported:false,refusals:[{code}]},
  actions:['apply','withdraw_proposal','pause','resume','resolve_outcome','rollback'].map(action=>({action,enabled:false,reason:code})),
  resume:{lift_pause:false,ready:false,reason:code}};
}

// The dashboard has no qualified read-only source of operation status.
// Show that limitation explicitly instead of constructing a scope, assuming
// maintenance state, probing a route, or treating another count as its charge.
export function stockApplyDiagnosticsUnavailable(){
 return unavailableView('status_source_unavailable');
}

function validOperation(raw){
 if(!exact(raw,operationKeys)||!/^[0-9a-f]{32}$/.test(raw.operation_id))return false;
 if(!scope(raw.scope)||typeof raw.pool_id!=='string'||raw.pool_id.length>256||typeof raw.repository_id!=='string'||raw.repository_id.length>256||
    typeof raw.source!=='string'||raw.source.length>256||typeof raw.fingerprint!=='string'||raw.fingerprint.length>256||
    !count(raw.observed)||raw.observed===0||!count(raw.suggested)||raw.suggested===0||!count(raw.charged)||
    !['increase','decrease'].includes(raw.direction)||!operationStates.has(raw.state)||typeof raw.drift!=='boolean'||
    ![raw.plan_expires_at,raw.created_at,raw.updated_at].every(timestamp))return false;
 return true;
}

export function stockApplyView(raw){
 if(!raw||typeof raw!=='object'||Array.isArray(raw))return unavailableView();
 const baseKeys=['scope','state','reason','paused','drift','actions','charged','capability','resume'];
 const keys=Object.keys(raw);
 if(keys.length!==baseKeys.length+(Object.hasOwn(raw,'operation')?1:0)||!baseKeys.every(k=>Object.hasOwn(raw,k)))return unavailableView();
 if(!scope(raw.scope)||!states.has(raw.state)||!reason(raw.reason)||typeof raw.paused!=='boolean'||typeof raw.drift!=='boolean'||raw.drift===true&&!raw.operation)return unavailableView();
 const chargedKeys=['runners','reason'];
 if(!exact(raw.charged,chargedKeys)||!count(raw.charged.runners)||!text(raw.charged.reason))return unavailableView();
 const refusals=raw.capability?.refusals===null?[]:raw.capability?.refusals;
 if(!exact(raw.capability,['supported','refusals'])||typeof raw.capability.supported!=='boolean'||!Array.isArray(refusals)||refusals.length>8)return unavailableView();
 for(const refusal of refusals){
  const keys=typeof refusal?.detail==='string'?['code','detail']:['code'];
  if(!exact(refusal,keys)||!refusalCodes.has(refusal.code)||refusal.detail!==undefined&&(!text(refusal.detail)||refusal.detail.length>128))return unavailableView();
 }
 if(raw.capability.supported!==(refusals.length===0))return unavailableView();
 if(!Array.isArray(raw.actions)||raw.actions.length!==actionNames.size)return unavailableView();
 const seen=new Set();
 for(const action of raw.actions){
  const keys=typeof action?.reason==='string'?['action','enabled','reason']:['action','enabled'];
  if(!exact(action,keys)||!actionNames.has(action.action)||seen.has(action.action)||typeof action.enabled!=='boolean'||
     action.reason!==undefined&&!reason(action.reason))return unavailableView();
  // Mutating Garm commands must never arrive enabled; such a payload is untrusted.
  if(alwaysDisabled.has(action.action)&&action.enabled)return unavailableView();
  seen.add(action.action);
 }
 if(seen.size!==actionNames.size)return unavailableView();
 const resumeKeys=['lift_pause','ready'];
 if(!exact(raw.resume,resumeKeys)&&!(exact(raw.resume,['lift_pause','lift_reason','ready'])||exact(raw.resume,['lift_pause','ready','reason'])||exact(raw.resume,['lift_pause','lift_reason','ready','reason'])))return unavailableView();
 if(typeof raw.resume.lift_pause!=='boolean'||typeof raw.resume.ready!=='boolean'||
    raw.resume.lift_reason!==undefined&&!reason(raw.resume.lift_reason)||raw.resume.reason!==undefined&&!reason(raw.resume.reason))return unavailableView();
 const operation=raw.operation===undefined?null:validOperation(raw.operation)?raw.operation:null;
 if(raw.operation!==undefined&&operation===null)return unavailableView();
 if(['proposed','applied_when_proven','outcome_unknown'].includes(raw.state)&&!operation)return unavailableView();
 if(raw.resume.lift_pause!==raw.paused||raw.resume.ready&&
    (!raw.capability.supported||raw.paused||raw.drift||raw.state!=='proposed'||operation?.state!=='proposed'||operation.drift))return unavailableView();
 return {state:raw.state,reason:raw.reason,paused:raw.paused,drift:raw.drift,operation,
  charged:raw.charged,capability:{supported:raw.capability.supported,refusals},actions:raw.actions,resume:raw.resume};
}

const refusalMessages={
 interface_unsupported:'The presented upstream interface is not a supported capability input.',
 stock_garm_v0_2_1_observe_only:'Stock GARM v0.2.1 provides no controller-specific pool-write role and no atomic conditional update. An ordinary stock PUT plus GET is neither conditional nor authoritative, so this path stays observe-only.',
 external_ownership_unproven:'Missing verified evidence that the controller exclusively owns the managed settings for this exact scope.',
 completion_proof_unavailable:'Missing verified evidence of an independently authoritative completion result that survives interruption, timeout and restart.',
 late_writer_fence_unproven:'Missing verified evidence that delayed or concurrent external writers cannot still settle after an outcome is recorded.',
 requirement_unestablished:null,
 physical_allocation_teardown_out_of_scope:'The candidate adapter reaches into physical allocation or teardown; only verified settings are in scope.',
 view_unavailable:'The controller view payload was malformed or untrusted and is shown as unavailable.',
 status_source_unavailable:'This console has no qualified read-only source for operation status.'};

export function stockApplyMessages(view){
 const state=({
  unavailable:'Stock settings apply is unavailable. No supported upstream write interface is qualified, so the controller stays observe-only and every command below is disabled.',
  proposed:'A change proposal is recorded for operator review. It has changed nothing upstream and grants no write authority.',
  paused_maintenance:'Controller settings changes are paused for maintenance. New work is blocked and pending or unknown operations are preserved, never silently cleared.',
  applied_when_proven:'The recorded change reached an independently proven outcome. That result describes the original request only; it is not a statement about current settings or released capacity.',
  outcome_unknown:'A settings change was interrupted and its outcome is unknown. A current read matching the old or new value is an observation, not proof of completion; the operation stays blocked and charged.'})[view.state]||'Stock settings apply is unavailable.';
 const charged=view.operation
  ?`Last recorded runner charge: ${view.charged.runners} slot(s). Not a live capacity reading. ${view.charged.reason}`
  :'Recorded runner charge: unavailable here. This is not a zero count.';
 const refusals=view.capability.refusals.map(refusal=>refusalMessages[refusal.code]??`Capability refused: ${refusal.code}.`);
 const detail=view.capability.refusals.find(refusal=>refusal.code==='requirement_unestablished');
 if(detail)refusals[view.capability.refusals.indexOf(detail)]=`A required adapter property is not established: ${detail.detail}.`;
 return {state,reason:view.reason,charged,refusals,
  drift:view.drift?'Observed settings drifted from the journaled operation; apply stays blocked pending explicit operator review.':'',
  resumeLift:view.resume.lift_pause
   ?'The recorded state allows lifting the local maintenance pause, but this panel cannot do it. Lifting it never clears, resolves or reclassifies a pending or unknown operation.'
   :view.paused===null?'Local maintenance pause status: unavailable here.'
    :`Lifting the local maintenance pause is unavailable (${(view.resume.lift_reason||'unavailable').replaceAll('_',' ')}).`,
  resumeReady:view.resume.ready
   ?'Ready to resume apply work: the pause is lifted and every fresh drift check passes. No apply control is enabled here without a qualified adapter.'
   :`Ready to resume apply work: no — ${(view.resume.reason||'unavailable').replaceAll('_',' ')}. Lifting a pause does not imply readiness; fresh drift checks run separately afterwards.`};
}
