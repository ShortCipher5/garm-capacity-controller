const states=new Set(['unconfigured','awaiting_first_cycle','processing','unresolved']);
const names=['Examined','AwaitingEvidence','OutsideScope','OperationsCompleted','Unresolved'];
export function validateNativeRetirementStatus(v){
 const s=v?.status,r=s?.Result,zero=s?.At==='0001-01-01T00:00:00Z';
 if(!v||v.scope!=='canonical_retirement_operations_only'||v.release_authorized!==false||!states.has(v.state)||!s||typeof s.Incomplete!=='boolean'||typeof s.At!=='string'||!Number.isFinite(Date.parse(s.At))||!r||r.ReleaseAuthorized!==false||names.some(k=>!Number.isSafeInteger(r[k])||r[k]<0||r[k]>64)||r.AwaitingEvidence+r.OutsideScope+r.OperationsCompleted+r.Unresolved>r.Examined||(['unconfigured','awaiting_first_cycle'].includes(v.state)!==zero)||(zero&&names.some(k=>r[k]!==0))||(v.state==='processing'&&(s.Incomplete||r.Unresolved>0))||(v.state==='unresolved'&&!s.Incomplete))throw new Error('Native retirement process status unavailable or unsupported.');
 return v;
}
export function retirementGuidance(state){return {unconfigured:'No native retirement service is configured in this process.',awaiting_first_cycle:'Configured service has not recorded a completed cycle. No operation is implied.',processing:'The last completed cycle reported no unresolved result. This does not describe a currently in-flight operation or physical release.',unresolved:'The last cycle was incomplete or contains unresolved operations. Preserve durable claims and receipts; do not retry, reset or release charges from this view.'}[state]||'Status unavailable.';}
