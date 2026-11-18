const states=new Set(['unconfigured','not_started','starting','running','waiting','failed','blocked','stopped']);
const integer=v=>Number.isSafeInteger(v)&&v>=0;
const time=v=>typeof v==='string'&&Number.isFinite(Date.parse(v));
export const hasEventTime=v=>time(v)&&!v.startsWith('0001-');
export function validateAllocatorStatus(v){
 const s=v?.status;
 if(v?.scope!=='process-grant-loop'||!s||!states.has(s.state)||!time(s.observed_at)||typeof s.in_flight!=='boolean'||!integer(s.attempt_count)||!integer(s.success_count)||s.success_count>s.attempt_count||!integer(s.last_grant_count)||['started_at','last_attempt_at','last_completed_at','last_success_at','stopped_at'].some(k=>s[k]!==undefined&&!time(s[k]))||s.last_batch_id!==undefined&&(typeof s.last_batch_id!=='string'||s.last_batch_id.length>128)||s.reason_code!==undefined&&!['','native_management_authority_unavailable','cycle_failed','cancelled'].includes(s.reason_code)||s.in_flight!==(s.state==='running')||['unconfigured','not_started','blocked'].includes(s.state)&&(s.attempt_count!==0||s.success_count!==0||hasEventTime(s.last_attempt_at)))throw new Error('Allocator status unavailable or unsupported.');
 return v;
}
export function allocatorGuidance(s){
 const messages={unconfigured:'No allocator service is configured in this gateway process.',not_started:'Configured service has not started in this process.',starting:'The service has started checking prerequisites; no cycle completion is implied.',running:'A grant-planning cycle is in flight. This is not runner creation or a pool-settings change.',waiting:'The last grant-planning cycle succeeded; the service is waiting for the next interval. Grants are not running workers.',failed:'The last cycle failed. Inspect controller diagnostics; this read does not retry or clear durable state.',blocked:'Runtime native/management authority is unavailable. Configuration cannot override this gate; no cycle was attempted.',stopped:'The service stopped in this process. Existing grants and runner jobs are not automatically cancelled.'};
 return messages[s.state]||'Status unavailable.';
}
