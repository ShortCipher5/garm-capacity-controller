const count=v=>Number.isSafeInteger(v)&&v>=0&&v<=1000000;
const id=v=>typeof v==='string'&&/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(v);
const hash=v=>typeof v==='string'&&/^[0-9a-f]{64}$/.test(v);
export function validateAllocatorPlan(v,now=Date.now()){
 const fail=()=>{throw new Error('Fresh allocation comparison unavailable. Observed counts and plan are unknown.');};
 if(!v||v.source!=='scoped-stock-and-coherent-ledger'||v.atomic!==false||!hash(v.source_identity)||!hash(v.demand_digest)||!Number.isSafeInteger(v.ledger_sequence)||v.ledger_sequence<0||!Number.isSafeInteger(v.policy_revision)||v.policy_revision<0||!count(v.remaining_jobs)||!Array.isArray(v.pools)||v.pools.length>1000)fail();
 const [start,end,captured,expires]=['started_at','finished_at','captured_at','expires_at'].map(k=>Date.parse(v[k]));
 if(![start,end,captured,expires].every(Number.isFinite)||start>end||end>captured||captured>now+1000||expires<=now||expires-start>60000||captured>=expires)fail();
 const seen=new Set();
 for(const p of v.pools){
  if(!id(p.pool)||!id(p.repository)||seen.has(p.pool)||!['cap','warm_idle','additional','additional_warm','covered_by_idle','covered_by_grant','deferred_by_inflight'].every(k=>count(p[k]))||p.warm_idle>p.cap||p.additional_warm>p.additional||!p.charged||!['cpu_millis','ram_mib','disk_mib','runners'].every(k=>Number.isSafeInteger(p.charged[k])&&p.charged[k]>=0))fail();
  seen.add(p.pool);
  if(p.supply!==null&&(!p.supply||p.supply.pool_id!==p.pool||!['idle','busy','creating','other'].every(k=>count(p.supply[k]))))fail();
 }
 return v;
}
export function allocatorPlanStale(v,now=Date.now()){return !v||Date.parse(v.expires_at)<=now;}
