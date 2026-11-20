import React,{useMemo} from 'react';
import {brokerPolicyDraftReview} from './broker-policy-review.js';

export function BrokerPolicyReview({saved,draft,stale}){
 const review=useMemo(()=>brokerPolicyDraftReview(saved,draft,stale),[saved,draft,stale]);
 return <details><summary>Compare draft with last loaded policy</summary>
 {review.state==='stale'?<p>Saved revision {saved?.revision??'unavailable'} is not verified as current. Reload policy before comparing or editing again.</p>:<>
  <p>Compared with saved revision {saved.revision}. Draft edits are not submitted and have no live effect. Allocation previews use the saved policy, not these draft edits.</p>
  {review.state==='invalid'&&<p>Complete valid, unique repository and pool IDs and settings to review exact changes.</p>}
  {review.state==='unchanged'&&<p>No policy values differ from this saved revision.</p>}
  {review.state==='changed'&&<div className="table-wrap"><table><thead><tr><th>Repository / pool</th><th>Setting</th><th>Saved before</th><th>Draft after</th></tr></thead><tbody>
   {review.rows.flatMap(({repository,pool,fields})=>fields.map(({label,before,after})=><tr key={`${repository}/${pool??''}/${label}`}><td>{repository}{pool&&<p>Pool {pool}</p>}</td><td>{label}</td><td>{before??'Not in saved policy'}</td><td>{after??'Removed from draft'}</td></tr>))}
  </tbody></table></div>}
 </>}
 </details>;
}
