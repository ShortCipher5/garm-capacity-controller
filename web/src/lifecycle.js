// Read-only explanations of recorded state. Never derive release or activation authority.
const states = {
 creating: ['Pending creation', 'Creation has not reached a confirmed usable state. Inspect the operation and reconcile before retrying; do not create a replacement merely because it is slow.'],
 deleting: ['Pending deletion', 'Deletion is not proof of absence. Keep the charge until authoritative reconciliation confirms completion.'],
 unknown: ['Outcome unknown', 'Inspect the original operation and backend evidence. Do not repeat creation, clear the ledger or release capacity based on a missing response.'],
 failed: ['Failure recorded', 'Inspect the original failure and reconcile any surviving resources. A failure does not prove nothing was created.'],
 idle: ['Last recorded idle', 'An idle observation is not a drain fence. The runner may accept another GitHub job.'],
 running: ['Last recorded running', 'Do not retire the runner based on this snapshot. Obtain current job and backend evidence.'],
 released: ['Released history', 'Historical ledger release, not proof that the resource remains absent now.'],
};
export function stateGuidance(state) {
 const [label, action] = states[state] || ['Unclassified recorded state', 'Inspect the recorded state and original operation. This console cannot infer completion or safe release.'];
 return {state, label, action};
}
const blockers = {
 new_ledger_claims_not_paused: 'New ledger claims are not paused. Review host policy before planning retirement; pausing does not stop existing jobs.',
 fresh_healthy_inventory_required: 'Obtain fresh healthy backend inventory. Discovery alone does not reconcile charges.',
 tracked_allocations_remain_charged: 'Tracked allocations remain charged. Reconcile their lifecycle instead of deleting ledger entries.',
 reservations_without_inventory_remain_charged: 'Reservations lack matching inventory. Resolve their original create outcomes; missing inventory is not proof of absence.',
 inventory_without_reservation_requires_reconciliation: 'Inventory has no matching reservation. Investigate attribution before adoption, deletion or release.',
 job_assignment_fence_unqualified: 'This assessment does not establish a GitHub job-assignment fence. Idle runners can still receive jobs.',
 confirmed_retirement_unimplemented: 'This assessment reports no confirmed retirement capability. Use the separately qualified retirement workflow, not host mode changes.',
};
export function blockerGuidance(code) { return blockers[code] || `Unrecognized blocker (${code}). Keep retirement blocked and inspect the backend assessment.`; }
export function hostPauseReasons(host, data) {
 const reasons=[];
 if(!data.experimental_provisioning_enabled) reasons.push('Provisioning is off in this snapshot.');
 if(!data.admission_ready) reasons.push('Admission barrier is not ready.');
 if(host.mode !== 'enabled') reasons.push(`Host mode is ${host.mode}; new claims are paused.`);
 if(host.backend_binding_status !== 'bound_observation') reasons.push(host.backend_binding_status === 'legacy_scope_import_required' ? 'Legacy backend scope requires a qualified import; mode changes cannot clear it.' : 'Backend observation binding is missing. Registration or discovery does not establish it.');
 if(!host.observation_fresh) reasons.push('Backend observation is stale or unavailable. Refresh authoritative inventory.');
 if(!host.last_observed_healthy) reasons.push('Last backend observation was unhealthy. Inspect the backend before resuming.');
 if(host.new_claims_paused && !reasons.length) reasons.push('New claims are paused; this snapshot does not explain the additional gate. Inspect the controller diagnostics.');
 return reasons;
}
export function drainView(value) {
 const integer=n=>Number.isSafeInteger(n)&&n>=0;
 if(!value||!integer(value.tracked_allocations)||!integer(value.unobserved_reservations)||!integer(value.unreserved_resources)||!Array.isArray(value.blockers)||value.blockers.length>64||!value.blockers.every(x=>typeof x==='string'&&x.length<=256))return null;
 return {tracked:value.tracked_allocations, unobserved:value.unobserved_reservations, unreserved:value.unreserved_resources, blockers:value.blockers.map(code=>({code,action:blockerGuidance(code)}))};
}
