// Verified runner teardown reuse status: what stays charged, what qualified
// evidence freed, and why the rest is held. This module never releases,
// deletes or retries anything; an unshown or unknown charge stays charged.
const dispositions=new Set(['charged','pending_cleanup','partially_reusable','verified_reusable','released_historic']);
const reasons=new Set(['awaiting_qualified_terminal_evidence','released_verified_teardown','awaiting_completed_workload','awaiting_terminal_delete_observation','awaiting_post_delete_absence','residual_volume_unverified','conflicting_evidence','chronology_conflict','evidence_for_distinct_charge','same_name_replacement_is_distinct_charge','invalid_charge','historic_release_requires_review']);
const labels=Object.freeze({
 awaiting_qualified_terminal_evidence:'Awaiting qualified terminal teardown evidence; the charge stays allocated.',
 released_verified_teardown:'Released once by scoped positive terminal proof; replay does not release again.',
 awaiting_completed_workload:'The assigned workload has no positive completed observation.',
 awaiting_terminal_delete_observation:'The captured original terminal DELETE operation is missing.',
 awaiting_post_delete_absence:'The two-pass post-delete absence observation is missing.',
 residual_volume_unverified:'An applicable charged volume still has no positive absence observation.',
 conflicting_evidence:'Delivered evidence conflicts, so the charge stays held.',
 chronology_conflict:'Delivered evidence has an impossible chronology.',
 evidence_for_distinct_charge:'Delivered evidence addresses a different charge.',
 same_name_replacement_is_distinct_charge:'A replacement reusing the display name is a distinct charge.',
 invalid_charge:'The charge identity is invalid.',
 historic_release_requires_review:'Excluded from accounting by a pre-verification release path; not verified reusable capacity. It needs explicit accounting and physical inventory review and is never reversed by this view.',
});
const dispositionLabels=Object.freeze({
 charged:'Charged',
 pending_cleanup:'Pending cleanup',
 partially_reusable:'Partially reusable',
 verified_reusable:'Verified reusable',
 released_historic:'Historic release (pre-verification)',
});
const dispositionGuidance=Object.freeze({
 charged:'Normal lifecycle charge; no cleanup is in progress.',
 pending_cleanup:'Teardown was requested, but no qualified terminal evidence exists yet. The charge stays allocated; a timeout, delete acknowledgement, reduced limit or missing row never releases it.',
 partially_reusable:'Qualified evidence freed only the listed dimensions. Every remaining dimension stays charged until its own positive evidence exists.',
 verified_reusable:'Every dimension was freed once by scoped positive terminal proof. This view cannot release anything else or re-bind the retired identity.',
 released_historic:'A previous release path excluded this charge from accounting before verified reuse existed. It is not verified reusable capacity, is never re-verified or reversed here, and requires explicit accounting and physical inventory review.',
});
export function reuseReasonLabel(reason){return labels[reason]||`Unrecognized hold reason (${reason}). Keep the charge held and inspect the ledger evidence.`;}
export function reuseDispositionLabel(disposition){return dispositionLabels[disposition]||'Status unavailable.';}
export function validateReuseStatus(view){
 const object=v=>v&&typeof v==='object'&&!Array.isArray(v);
 const integer=v=>Number.isSafeInteger(v)&&v>=0;
 const bool=v=>typeof v==='boolean';
 const resources=v=>object(v)&&['cpu_millis','ram_mib','disk_mib','runners'].every(k=>integer(v[k]));
 const dimensions=v=>object(v)&&['cpu','ram','runners','disk'].every(k=>bool(v[k]));
 const zero=v=>v.cpu_millis===0&&v.ram_mib===0&&v.disk_mib===0&&v.runners===0;
 if(!object(view)||typeof view.observed_at!=='string'||!Number.isFinite(Date.parse(view.observed_at))||!Array.isArray(view.rows)||!view.rows.every(row=>object(row)&&typeof row.request_key==='string'&&row.request_key.length>0&&typeof row.pool==='string'&&typeof row.host==='string'&&typeof row.state==='string'&&dispositions.has(row.disposition)&&resources(row.remaining)&&dimensions(row.reusable)&&Array.isArray(row.reasons)&&row.reasons.length<=16&&new Set(row.reasons).size===row.reasons.length&&row.reasons.every(r=>typeof r==='string'&&reasons.has(r))))throw new Error('Reuse status unavailable or unsupported.');
 for(const row of view.rows){
  const freed=row.reusable;
  if(row.disposition==='verified_reusable'&&!(freed.cpu&&freed.ram&&freed.runners&&freed.disk&&zero(row.remaining)))throw new Error('Reuse status unavailable or unsupported.');
  if(row.disposition==='partially_reusable'&&!(freed.cpu||freed.ram||freed.runners||freed.disk)&&!zero(row.remaining))throw new Error('Reuse status unavailable or unsupported.');
  if(row.disposition==='charged'&&row.reasons.length!==0)throw new Error('Reuse status unavailable or unsupported.');
  if(row.disposition==='pending_cleanup'&&(row.reasons.length!==1||row.reasons[0]!=='awaiting_qualified_terminal_evidence'))throw new Error('Reuse status unavailable or unsupported.');
  if(row.disposition==='verified_reusable'&&(row.reasons.length!==1||row.reasons[0]!=='released_verified_teardown'))throw new Error('Reuse status unavailable or unsupported.');
  if(row.disposition==='released_historic'&&((row.reusable.cpu||row.reusable.ram||row.reusable.runners||row.reusable.disk)||!zero(row.remaining)||row.reasons.length!==1||row.reasons[0]!=='historic_release_requires_review'))throw new Error('Reuse status unavailable or unsupported.');
 }
 return view;
}
