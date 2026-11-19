import {validPreflightRecipe,validateNativePreflight} from './native-preflight-client.js';
const hash = v => typeof v === 'string' && /^[0-9a-f]{64}$/.test(v);
const fingerprint = v => typeof v === 'string' && /^SHA256:[A-Za-z0-9+/]{43}$/.test(v);
const text = (v, max) => typeof v === 'string' && v.length > 0 && v.length <= max && !/[\x00-\x1f\x7f]/.test(v);
const timestamp = v => typeof v === 'string' && /^\d{4}-\d\d-\d\dT/.test(v) && Number.isFinite(Date.parse(v)) && Date.parse(v) > 0;
function validReceipt(r, target) { return r?.version === 'native-owner-preparation/v1' && r.id === target.preparation_id && r.state === 'prepared_not_activated' && r.manifest_digest === target.manifest_digest && hash(r.unit_digest) && r.activation_authorized === false; }
export function validPreparationAttempt(a, target) {
  if (!a || !Number.isSafeInteger(a.sequence) || a.sequence < 1 || a.sequence > 1024 || !['prepare','inspect'].includes(a.mode) || a.review_digest !== target.review_digest || !timestamp(a.started_at)) return false;
  if (a.status === 'unknown') return a.completed_at === undefined && a.receipt === undefined;
  if (!timestamp(a.completed_at) || Date.parse(a.completed_at) < Date.parse(a.started_at)) return false;
  if (a.status === 'observation_unavailable') return a.receipt === undefined;
  return a.status === 'prepared_not_activated' && validReceipt(a.receipt, target);
}
export function validPreparationState(v, target) {
  if (!v || v.review_digest !== target.review_digest || !['not_started', 'unknown', 'prepared_not_activated'].includes(v.status)) return false;
  if (v.attempt !== undefined && (!validPreparationAttempt(v.attempt,target) || v.status === 'not_started')) return false;
  return v.status === 'prepared_not_activated' ? validReceipt(v.receipt,target) : v.receipt === undefined;
}
export function validPreparedDiagnostic(t){return validPreflightRecipe(t?.diagnostic)&&t.diagnostic.preparation_id===t.preparation_id&&t.diagnostic.manifest_digest===t.manifest_digest&&t.state?.status==='prepared_not_activated'&&validReceipt(t.state.receipt,t);}
export function validatePreparationView(v) {
  if (!Array.isArray(v?.targets) || v.targets.length > 8 || new Set(v.targets.map(t => t?.target_id)).size !== v.targets.length || !v.targets.every(t => text(t.target_id, 64) && text(t.address, 45) && text(t.admin_user, 32) && fingerprint(t.host_key_fingerprint) && fingerprint(t.admin_key_fingerprint) && hash(t.manifest_digest) && hash(t.script_sha256) && Number.isInteger(t.port) && t.port > 0 && t.port <= 65535 && hash(t.preparation_id) && hash(t.review_digest) && Number.isSafeInteger(t.bundle_bytes) && t.bundle_bytes > 0 && t.bundle_bytes <= 512 * 1024 * 1024 && t.activation_authorized === false && validPreparationState(t.state, t) && (t.diagnostic===undefined||validPreparedDiagnostic(t)))) throw new Error('Unsupported native preparation status. Clear the review and reload.');
  return v;
}
export async function nativePreparationRequest(token, target = null, inspectOnly = false, {fetcher = fetch, signal} = {}) {
  if (!hash(token)) throw new Error('Enter the separate native preparation token.');
  if (target && (!validatePreparationView({targets:[target]}) || typeof inspectOnly !== 'boolean' || (!inspectOnly && (target.state.status !== 'not_started' || target.state.attempt)) || (inspectOnly && target.state.status === 'not_started') || target.state.attempt?.sequence === 1024)) throw new Error('Reload and review the exact preparation action. Unknown outcomes permit inspection only, never another upload.');
  const body = target ? {target_id:target.target_id, review_digest:target.review_digest, confirm_prepare:true, inspect_only:inspectOnly} : null;
  let response;
  try { response = await fetcher('/admin/api/v1/native-preparation', {method:body ? 'POST' : 'GET', headers:{Authorization:`Bearer ${token}`, Accept:'application/json', ...(body ? {'Content-Type':'application/json'} : {})}, ...(body ? {body:JSON.stringify(body)} : {}), credentials:'omit', redirect:'error', cache:'no-store', signal}); }
  catch { throw new Error('Preparation outcome unknown. Closing or cancelling does not undo a transfer. Reload durable status; never upload again after an uncertain request.'); }
  if (response.status !== (body ? 202 : 200)) throw new Error(response.status === 401 ? 'Native preparation token rejected.' : response.status === 404 ? 'Native preparation is not configured for this deployment.' : 'Preparation request unavailable or rejected. Reload durable status; a failed response is not proof of no effect.');
  let value;
  try { value = await response.json(); } catch { throw new Error('Preparation outcome unknown: invalid response. Reload durable status without repeating the upload.'); }
  if (!body) return validatePreparationView(value);
  if (value?.target_id !== target.target_id || value.state?.status !== 'unknown' || !validPreparationState(value.state, target)) throw new Error('Preparation outcome unknown: response does not match the reviewed target. Reload durable status without repeating the upload.');
  const attempt = value.state.attempt;
  if ((target.state.attempt && !attempt) || (attempt && (attempt.sequence !== (target.state.attempt?.sequence || 0) + 1 || attempt.mode !== (inspectOnly ? 'inspect' : 'prepare') || attempt.status !== 'unknown'))) throw new Error('Preparation outcome unknown: accepted attempt does not match the reviewed action. Reload durable status without repeating the upload.');
  return value;
}

export async function preparedDiagnosticRequest(token,target,{fetcher=fetch,signal}={}){
 if(!hash(token)||!validPreparedDiagnostic(target))throw Error('Load the exact prepared diagnostic recipe and enter the separate preparation token.');
 validatePreparationView({targets:[target]});
 const unavailable='Prepared diagnostic unavailable or cancelled. No report is confirmed. Reload the reviewed target before another explicit read; no automatic retry was made.';
 let r,v;try{r=await fetcher('/admin/api/v1/native-preparation/diagnostic',{method:'POST',headers:{Authorization:`Bearer ${token}`,'Content-Type':'application/json',Accept:'application/json'},body:JSON.stringify({target_id:target.target_id,preparation_id:target.preparation_id,review_digest:target.diagnostic.review_digest,confirm_read:true}),credentials:'omit',redirect:'error',cache:'no-store',signal});if(r.status!==200||signal?.aborted)throw Error();v=await r.json();if(signal?.aborted)throw Error();}catch{throw Error(unavailable);}
 if(v?.target_id!==target.target_id||v.preparation_id!==target.preparation_id||v.review_digest!==target.diagnostic.review_digest)throw Error('Diagnostic response does not match the reviewed prepared bundle. No report is confirmed.');
 return validateNativePreflight(v.report);
}
