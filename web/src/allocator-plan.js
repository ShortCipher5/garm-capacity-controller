const count=v=>Number.isSafeInteger(v)&&v>=0&&v<=1000000;
const id=v=>typeof v==='string'&&/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(v);
const hash=v=>typeof v==='string'&&/^[0-9a-f]{64}$/.test(v);
export function validateAllocatorPlan(v,now=Date.now()){
 const fail=()=>{throw new Error('Fresh allocation comparison unavailable. Observed counts and plan are unknown.');};
 if(!v||v.source!=='scoped-stock-and-coherent-ledger'||v.atomic!==false||!hash(v.source_identity)||!hash(v.demand_digest)||!Number.isSafeInteger(v.ledger_sequence)||v.ledger_sequence<0||!Number.isSafeInteger(v.policy_revision)||v.policy_revision<0||!count(v.remaining_jobs)||!Array.isArray(v.pools)||v.pools.length>1000)fail();
 const [start,end,captured,expires]=['started_at','finished_at','captured_at','expires_at'].map(k=>Date.parse(v[k]));
 if(![start,end,captured,expires].every(Number.isFinite)||start>end||end>captured||captured>now+1000||expires<=now||expires-start>60000||captured>=expires)fail();
 if(v.pressure!==undefined){
  if(!Array.isArray(v.pressure)||v.pressure.length>64)fail();
  const hosts=new Set();
  for(const p of v.pressure){if(!p||typeof p.host!=='string'||!p.host||p.host.length>256||hosts.has(p.host)||!['missing_or_stale_not_headroom','window_overflow_suppressed','partial_sample_not_headroom','high_partial_pressure_suppressed'].includes(p.state))fail();hosts.add(p.host);}
 }
 const seen=new Set();
 for(const p of v.pools){
  if(!id(p.pool)||!id(p.repository)||seen.has(p.pool)||!['cap','warm_idle','additional','additional_warm','covered_by_idle','covered_by_grant','deferred_by_inflight'].every(k=>count(p[k]))||p.warm_idle>p.cap||p.additional_warm>p.additional||!p.charged||!['cpu_millis','ram_mib','disk_mib','runners'].every(k=>Number.isSafeInteger(p.charged[k])&&p.charged[k]>=0))fail();
  if(p.suppressed_by_pressure!==undefined&&!count(p.suppressed_by_pressure))fail();
  seen.add(p.pool);
  if(p.supply!==null&&(!p.supply||p.supply.pool_id!==p.pool||!['idle','busy','creating','other'].every(k=>count(p.supply[k]))))fail();
 }
 return v;
}
export function allocatorPlanStale(v,now=Date.now()){return !v||Date.parse(v.expires_at)<=now;}

// Both sequences come from the controller's saved-ledger snapshot. An equal
// sequence is only a linked captured comparison, not evidence of a live ledger.
export function allocatorComparisonState(view,{policyRevision,ledgerSequence,policyStale=false,now=Date.now()}={}){
 if(!view)return "unavailable";
 if(allocatorPlanStale(view,now))return "preview_expired";
 if(policyStale)return "saved_comparison_stale";
 if(!Number.isSafeInteger(policyRevision)||policyRevision<0)return "saved_comparison_unavailable";
 if(policyRevision!==view.policy_revision)return "policy_changed";
 if(!Number.isSafeInteger(ledgerSequence)||ledgerSequence<0)return "saved_comparison_unavailable";
 if(ledgerSequence>view.ledger_sequence)return "preview_superseded";
 if(ledgerSequence<view.ledger_sequence)return "saved_comparison_older";
 return "linked_at_capture";
}

const comparisonMessages = Object.freeze({
 unavailable: "Observed runner counts and demand-derived plan are unknown.",
 preview_expired: "This preview expired. The counts and plan below are historical.",
 saved_comparison_stale: "The latest saved comparison could not be verified. Do not use the counts and plan below as a current comparison.",
 policy_changed: "The saved policy revision differs. The counts and plan below are not a current comparison.",
 saved_comparison_unavailable: "No matching saved ledger snapshot is available for this advisory preview.",
 preview_superseded: "A newer saved ledger snapshot is displayed. The counts and plan below are historical even if the saved policy revision did not change.",
 saved_comparison_older: "This preview used a later ledger sequence than the saved comparison above. Reload the saved policy and ledger before comparing the figures.",
 linked_at_capture: "The preview and saved comparison report the same policy revision and ledger sequence. These figures are captured observations, not evidence of current execution.",
});
export const allocatorComparisonMessage = state => Object.hasOwn(comparisonMessages,state) ? comparisonMessages[state] : comparisonMessages.unavailable;


const previewReasonMessages = Object.freeze({
 pressure_evidence_unavailable: ['unavailable', 'Required pressure evidence is unavailable or unsuitable for this preview. This does not identify a particular host fault or prove spare capacity.'],
 incomplete_evidence: ['unavailable', 'The captured stock scaling evidence is incomplete. No maximum suggestions are shown.'],
 evidence_unavailable: ['unavailable', 'Required stock scaling evidence is unavailable. No maximum suggestions are shown.'],
 awaiting_matching_sample: ['no_confirmed_proposals', 'Waiting for another observation with the same effective configuration before comparing maximum suggestions.'],
 configured_maximum_fit_veto: ['no_confirmed_proposals', 'No increase was suggested because the modeled runner load would exceed a configured budget in at least one observation. This is a modeled limit, not a measured host limit or a settings change.'],
 configured_maximum_fit_unavailable: ['no_confirmed_proposals', 'The configured maximums could not be compared for an increase. No increase was suggested; this does not establish a capacity limit.'],
 awaiting_cadence_window: ['no_confirmed_proposals', 'Waiting for a longer span of matching observations before comparing a smaller batch.'],
 no_confirmed_change: ['no_confirmed_proposals', 'The captured comparison confirmed no maximum change. This does not prove that no change is needed or that every comparison prerequisite was satisfied.'],
});
const previewStates = new Set(["unavailable", "no_confirmed_proposals", "proposed"]);
const exactKeys = (value, keys) => value && typeof value === "object" && !Array.isArray(value) &&
 Object.keys(value).length === keys.length && keys.every(key => Object.hasOwn(value, key));
const wireTimestamp = value => typeof value === "string" &&
 /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$/.test(value) ? Date.parse(value) : NaN;

// The ordinary allocator comparison remains usable when its optional preview
// is missing, malformed or stale. Never render a proposal against a different
// saved ledger comparison, or after its independent evidence deadline.
export function stockMaximumPreviewView(plan, comparison, now=Date.now()){
 return stockMaximumView(plan,plan?.stock_maximum_preview,comparison,now,false);
}
export function stockMaximumBatchPreviewView(plan, comparison, now=Date.now()){
 const batch=stockMaximumView(plan,plan?.stock_maximum_batch_preview,comparison,now,true);
 if(batch?.state!=="proposed")return batch;
 const regular=stockMaximumPreviewView(plan,comparison,now);
 if(regular?.state!=="proposed"||batch.expiresAt>regular.expiresAt||batch.proposals.some(p=>!regular.proposals.some(r=>(
  r.pool_id===p.pool_id&&r.repository_id===p.repository_id&&r.observed===p.observed&&r.suggested===p.suggested&&r.observed_min_idle===p.observed_min_idle))))return {state:"unavailable",proposals:[]};
 return batch;
}
function stockMaximumView(plan,preview,comparison,now,batch){
 if(preview===undefined)return null;
 const unavailable={state:"unavailable",proposals:[]};
 const keys=["state","expires_at","proposals"];
 if(preview&&Object.hasOwn(preview,"reason"))keys.push("reason");
 if(preview&&Object.hasOwn(preview,"host_evidence"))keys.push("host_evidence");
 if(!exactKeys(preview,keys)||(Object.hasOwn(preview,"host_evidence")&&(preview.host_evidence!=="configured_only"||preview.state==="unavailable"))||!previewStates.has(preview.state)||
  !Array.isArray(plan.pools)||!Array.isArray(preview.proposals)||preview.proposals.length>plan.pools.length||(batch&&preview.proposals.length>2)||
  (batch&&preview.state!=="unavailable"&&preview.host_evidence!=="configured_only")||
  (preview.state==="proposed")!==(preview.proposals.length>0))return unavailable;
 const expires=wireTimestamp(preview.expires_at),captured=Date.parse(plan.captured_at),planExpiry=Date.parse(plan.expires_at);
 if(![expires,captured,planExpiry,now].every(Number.isFinite)||expires<=captured||expires>planExpiry)return unavailable;
 const repositories=new Map(plan.pools.map(({pool,repository})=>[pool,repository])),seen=new Set();
 for(const row of preview.proposals){
  if(!(exactKeys(row,["pool_id","repository_id","observed","suggested"])||exactKeys(row,["pool_id","repository_id","observed","suggested","observed_min_idle"]))||!id(row.pool_id)||!id(row.repository_id)||
   repositories.get(row.pool_id)!==row.repository_id||seen.has(row.pool_id)||!count(row.observed)||!count(row.suggested)||
   row.observed===0||row.suggested===0||Math.abs(row.observed-row.suggested)!==1||
   (Object.hasOwn(row,"observed_min_idle")&&(!Number.isSafeInteger(row.observed_min_idle)||row.observed_min_idle<0||row.observed_min_idle>1000000||row.observed_min_idle>row.observed||row.observed_min_idle>row.suggested)))return unavailable;
  seen.add(row.pool_id);
 }
 if(expires<=now||captured>now+1000)return {state:"expired",proposals:[]};
 if(comparison!=="linked_at_capture")return {state:"comparison_unavailable",proposals:[]};
 const reason=(batch||preview.reason!=="awaiting_cadence_window")&&typeof preview.reason==="string"&&Object.hasOwn(previewReasonMessages,preview.reason)&&previewReasonMessages[preview.reason][0]===preview.state?preview.reason:null;
 return {state:preview.state,expiresAt:expires,proposals:preview.proposals,...(reason?{reason}:{}),...(preview.host_evidence==="configured_only"?{hostEvidence:"configured_only"}:{})};
}

export function stockMaximumPreviewMessage(state,reason){
 if(typeof reason==="string"&&Object.hasOwn(previewReasonMessages,reason)&&previewReasonMessages[reason][0]===state)return previewReasonMessages[reason][1];
 switch(state){
 case "proposed": return "Two captured stock observations support these suggested maximums.";
 case "no_confirmed_proposals": return "No maximum changes were confirmed by the current advisory comparison. This does not prove none are needed.";
 case "expired": return "The stock maximum preview expired. Read a fresh allocation plan; no historical suggestions are shown.";
 case "comparison_unavailable": return "The saved ledger comparison does not match this preview. Reload it before using any suggestions.";
 default: return "The optional stock maximum preview is unavailable. No suggestions are shown.";
 }
}

const readinessMessages = Object.freeze({
 source_not_configured: 'The advisory preview source is not configured.',
 preview_unavailable: 'The advisory preview is unavailable.',
 observation_unavailable: 'A fresh stock runner observation is unavailable.',
 ledger_unavailable: 'The required ledger snapshot is unavailable.',
 pool_mapping_missing: 'A required enrolled pool mapping is missing.',
 host_binding_unavailable: 'A required host binding is unavailable or has changed.',
 enrollment_or_evidence_unavailable: 'Required enrollment or supporting evidence is unavailable.',
 planning_unavailable: 'The advisory calculation could not be completed.',
 snapshot_changed_or_expired: 'The observation changed or expired during the preview.',
 awaiting_observation: 'The advisory preview is waiting for its first observation.',
 preview_stopped: 'The advisory preview process has stopped.',
});
export class AllocatorReadinessError extends Error {
 constructor(body) {
  super('Allocation plan unavailable.');
  this.reason = body && !Array.isArray(body) && body.state === 'unavailable' &&
    typeof body.reason === 'string' && Object.hasOwn(readinessMessages, body.reason) ? body.reason : null;
 }
}
export function allocatorFailureMessage(error) {
 const generic = 'Current observation and plan are unknown. The source, enrolled mapping or ledger may be unavailable or changed. No writes were attempted.';
 if (!(error instanceof AllocatorReadinessError) || typeof error.reason !== 'string' || !Object.hasOwn(readinessMessages, error.reason)) return generic;
 return `Current observation and plan are unknown. ${readinessMessages[error.reason]} No writes were attempted. This is advisory readiness, not permission to start runners or release capacity.`;
}
