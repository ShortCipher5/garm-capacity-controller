const integer=v=>Number.isSafeInteger(v)&&v>=0;
const time=v=>v===undefined||typeof v==='string'&&Number.isFinite(Date.parse(v));
export function validatePolicyCapStatus(v){if(!v||!['not_started','running','stopped','blocked'].includes(v.state)||!integer(v.policy_revision)||!integer(v.confirmed)||v.confirmed>10||!integer(v.skipped)||v.skipped>1000||!time(v.last_attempt)||!time(v.last_success)||(v.last_request_id!==undefined&&v.last_request_id!==''&&!/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(v.last_request_id)))throw new Error('Unsupported policy-cap reconciliation status.');return v;}
export const capEventTime=v=>typeof v==='string'&&!v.startsWith('0001-')?new Date(v).toLocaleString():'not recorded';
