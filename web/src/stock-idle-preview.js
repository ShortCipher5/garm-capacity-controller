const count=v=>Number.isSafeInteger(v)&&v>=0&&v<=1000000;
const id=v=>typeof v==='string'&&/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(v);
const exact=(v,keys)=>v&&typeof v==='object'&&!Array.isArray(v)&&Object.keys(v).length===keys.length&&keys.every(k=>Object.hasOwn(v,k));
const timestamp=v=>typeof v==='string'&&/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$/.test(v)?Date.parse(v):NaN;
export function stockIdlePreviewView(plan,comparison,now=Date.now()){
 const p=plan?.stock_idle_preview;
 if(p===undefined)return null;
 const unavailable={state:'unavailable',proposals:[]},keys=['state','expires_at','proposals'];
 if(p&&Object.hasOwn(p,'reason'))keys.push('reason');
 if(p&&Object.hasOwn(p,'host_evidence'))keys.push('host_evidence');
 if(!exact(p,keys)||!['unavailable','no_confirmed_proposals','proposed'].includes(p.state)||!Array.isArray(p.proposals)||p.proposals.length>8||!Array.isArray(plan.pools)||(p.state==='proposed')!==(p.proposals.length>0))return unavailable;
 if(p.state==='unavailable'?Object.hasOwn(p,'host_evidence'):p.host_evidence!=='configured_only')return unavailable;
 const expires=timestamp(p.expires_at),captured=Date.parse(plan.captured_at),parentExpiry=Date.parse(plan.expires_at);
 if(![expires,captured,parentExpiry,now].every(Number.isFinite)||expires<=captured||expires>parentExpiry)return unavailable;
 const repos=new Map(plan.pools.map(row=>[row.pool,row.repository])),seen=new Set();
 for(const row of p.proposals){
  if(!exact(row,['pool_id','repository_id','observed_min_idle','suggested_min_idle','unchanged_maximum'])||!id(row.pool_id)||!id(row.repository_id)||repos.get(row.pool_id)!==row.repository_id||seen.has(row.pool_id)||!count(row.observed_min_idle)||row.observed_min_idle===0||!count(row.suggested_min_idle)||row.suggested_min_idle!==row.observed_min_idle-1||!count(row.unchanged_maximum)||row.unchanged_maximum<row.observed_min_idle)return unavailable;
  seen.add(row.pool_id);
 }
 if(expires<=now||captured>now+1000)return {state:'expired',proposals:[]};
 if(comparison!=='linked_at_capture')return {state:'comparison_unavailable',proposals:[]};
 const reason=typeof p.reason==='string'&&((p.state==='unavailable'&&p.reason==='evidence_unavailable')||(p.state==='no_confirmed_proposals'&&p.reason==='awaiting_matching_sample'))?p.reason:null;
 return {state:p.state,proposals:p.proposals,expiresAt:expires,...(reason?{reason}:{}),...(p.host_evidence==='configured_only'?{hostEvidence:'configured_only'}:{})};
}
export function stockIdlePreviewMessage(view){
 if(view.state==='no_confirmed_proposals'&&view.reason==='awaiting_matching_sample')return 'No minimum-idle decrease is confirmed. Another eligible matching observation is required; this does not prove an empty queue.';
 return ({
  proposed:'Two captured observations support these minimum-idle decrease suggestions.',
  no_confirmed_proposals:'No minimum-idle decrease was confirmed. This does not prove no change is needed.',
  expired:'The minimum-idle preview expired. Read a fresh allocation plan; no historical suggestions are shown.',
  comparison_unavailable:'The saved ledger comparison does not match this minimum-idle preview. Reload before comparing suggestions.',
 })[view.state]||'The optional minimum-idle preview is unavailable. No suggestions are shown.';
}
