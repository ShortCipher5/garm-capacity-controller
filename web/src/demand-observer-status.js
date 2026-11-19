const states=new Set(['unconfigured','not_started','observing','observed','unavailable','stale','stopped']);
const statuses=new Set(['observed','configuration_unavailable','garm_unavailable','github_or_checkpoint_unavailable']);
export const demandCountFields=['stock_queued','observed_demand','reader_only','retained_history','conflicted','missing_from_reader','pools','idle','creating','busy','unknown'];
const time=v=>typeof v==='string'&&Number.isFinite(Date.parse(v))&&Date.parse(v)>0;
const integer=v=>Number.isSafeInteger(v)&&v>=0;
export function validateDemandObserverStatus(v){
 const e=v?.event;
 if(!v||!states.has(v.state)||!time(v.observed_at)||v.source!=='scoped-stock-and-github-observation'||v.allocation_authorized!==false||v.expires_at!==undefined&&!time(v.expires_at))throw Error('Demand observation status unavailable or unsupported.');
 if(e!==undefined&&(!e||e.mode!=='observation_only'||!statuses.has(e.status)||!time(e.started_at)||!time(e.finished_at)||Date.parse(e.finished_at)<Date.parse(e.started_at)||e.atomic!==false||e.missed_webhook_coverage!==false||!integer(e.next_poll_seconds)||e.next_poll_seconds>86400||e.last_success_at!==undefined&&!time(e.last_success_at)))throw Error('Demand observation provenance is invalid.');
 if(e?.counts!==undefined&&(!e.counts||!demandCountFields.every(k=>integer(e.counts[k]))||e.status!=='observed'))throw Error('Demand counts unavailable or unsupported.');
 if(e?.counts&&(e.counts.reader_only>e.counts.observed_demand-e.counts.retained_history||e.counts.conflicted>e.counts.observed_demand||e.counts.missing_from_reader>e.counts.observed_demand))throw Error('Demand provenance counts are inconsistent.');
 if(v.state==='observed'&&(!e?.counts||!time(v.expires_at)||Date.parse(v.expires_at)<=Date.parse(e.started_at)||Date.parse(v.expires_at)>Date.parse(e.started_at)+300000))throw Error('Demand observation freshness is unavailable.');
 if(['unavailable','stale','stopped','unconfigured','not_started'].includes(v.state)&&e?.counts!==undefined)throw Error('Historical counts cannot be presented as current demand.');
 return v;
}
export function demandDisplayState(v,now=Date.now()){return v.state==='observed'&&now>=Date.parse(v.expires_at)?'stale':v.state;}
export function currentDemandCounts(v,now=Date.now()){return demandDisplayState(v,now)==='observed'?v.event?.counts:null;}
export const demandGuidance={unconfigured:'No demand observer is configured.',not_started:'Configured observer has not started.',observing:'A source observation is in progress. Previous counts are not shown.',observed:'Last completed scoped observation; not an atomic queue snapshot.',unavailable:'The latest observation was unavailable. No current demand total is shown.',stale:'The observation freshness window has expired. Counts are hidden until a fresh successful observation.',stopped:'The observer has stopped. Historical success does not establish current demand.'};
