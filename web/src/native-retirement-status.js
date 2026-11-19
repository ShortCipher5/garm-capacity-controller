const states=new Set(['unconfigured','awaiting_first_cycle','processing','unresolved']);
const freshnessStates=new Set(['no_cycle','clock_invalid','stale','within_cycle_window']);
const names=['Examined','AwaitingEvidence','OutsideScope','OperationsCompleted','Unresolved'];
export function validateNativeRetirementStatus(v){
 const s=v?.status,r=s?.Result,zero=s?.At==='0001-01-01T00:00:00Z';
 if(!v||v.scope!=='canonical_retirement_operations_only'||v.release_authorized!==false||!states.has(v.state)||(v.freshness!==undefined&&(!freshnessStates.has(v.freshness)||(v.freshness==='no_cycle')!==zero))||!s||typeof s.Incomplete!=='boolean'||typeof s.At!=='string'||!Number.isFinite(Date.parse(s.At))||!r||r.ReleaseAuthorized!==false||names.some(k=>!Number.isSafeInteger(r[k])||r[k]<0||r[k]>64)||r.AwaitingEvidence+r.OutsideScope+r.OperationsCompleted+r.Unresolved>r.Examined||(['unconfigured','awaiting_first_cycle'].includes(v.state)!==zero)||(zero&&names.some(k=>r[k]!==0))||(v.state==='processing'&&(s.Incomplete||r.Unresolved>0))||(v.state==='unresolved'&&!s.Incomplete))throw new Error('Native retirement process status unavailable or unsupported.');
 return v;
}
export function retirementGuidance(state){return {unconfigured:'No native retirement service is configured in this process.',awaiting_first_cycle:'Configured service has not recorded a completed cycle. No operation is implied.',processing:'The last completed cycle reported no unresolved result. This does not describe a currently in-flight operation or physical release.',unresolved:'The last cycle was incomplete or contains unresolved operations. Preserve durable claims and receipts; do not retry, reset or release charges from this view.'}[state]||'Status unavailable.';}
export function retirementFreshnessGuidance(freshness){return {no_cycle:'No completed cycle was recorded.',clock_invalid:'The cycle timestamp is ahead of the server clock. Treat this result as historical until the clock is verified.',stale:'The cycle was overdue when fetched. Treat the displayed counts as historical.',within_cycle_window:'The cycle was within its expected window when fetched. This does not prove the worker is running.'}[freshness]||'Cycle freshness is unavailable; treat this result as historical.';}

const reuseLabels=Object.freeze({
 cycle_stale:'The recorded cycle was overdue when fetched.',
 cycle_clock_unverified:'The recorded cycle clock is unverified.',
 cycle_incomplete:'The recorded cycle was incomplete.',
 canonical_root_fence_missing:'Durable canonical root-fence evidence was missing.',
 outside_configured_owner_scope:'Some bindings were outside the configured owner scope.',
 operation_outcome_unresolved:'Some operation outcomes remained unresolved.',
 retained_diagnostics_unavailable:'Retained diagnostics were unavailable.',
 retained_diagnostics_are_not_release_proof:'Recorded retained diagnostics do not prove physical release.',
 retained_diagnostics_not_recorded:'Some completed operations had no recorded retained diagnostics.',
 physical_capacity_reuse_not_established:'Physical capacity reuse was not established.',
});
export function retirementReuseView(view){
 const value=view?.capacity_reuse;
 if(value===undefined)return null;
 const unknown={historical:true,labels:['Capacity reuse diagnostics are unavailable or unsupported. No capacity reuse is established.']};
 if(!value||Array.isArray(value)||Object.keys(value).length!==3||value.reuse_authorized!==false||!Array.isArray(value.reasons)||value.reasons.length<1||value.reasons.length>10||new Set(value.reasons).size!==value.reasons.length||!value.reasons.every(r=>typeof r==='string'&&Object.hasOwn(reuseLabels,r)))return unknown;
 if(value.availability==='historical_only'){
  const expected={stale:'cycle_stale',clock_invalid:'cycle_clock_unverified'}[view.freshness];
  if(value.reasons.length!==1||value.reasons[0]!==expected)return unknown;
  return {historical:true,labels:value.reasons.map(r=>reuseLabels[r])};
 }
 if(value.availability!=='last_cycle_diagnostics'||view.freshness!=='within_cycle_window'||!value.reasons.includes('physical_capacity_reuse_not_established')||value.reasons.some(r=>r==='cycle_stale'||r==='cycle_clock_unverified'))return unknown;
 return {historical:true,labels:value.reasons.map(r=>reuseLabels[r])};
}
