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
