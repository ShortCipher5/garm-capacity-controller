import test from 'node:test';import assert from 'node:assert/strict';import {sshTargetsRequest} from './ssh-targets-client.js';
const token='d'.repeat(64),config={address:'192.0.2.20',port:22,user:'inventory',credential_ref:'key',host_key:'public'};
const target={id:'new',...config,host_key_fingerprint:'SHA256:'+'a'.repeat(43),review_digest:'b'.repeat(64)};
test('registration uses separate route without cookies and validates exact tuple',async()=>{
 let sent;const body={action:'review',target_id:'new',config};const value=await sshTargetsRequest(token,body,{fetcher:async(url,options)=>{sent={url,options};return{ok:true,json:async()=>({target,saved:false})}}});assert.equal(value.target.id,'new');assert.equal(sent.url,'/admin/api/v1/ssh-targets');assert.equal(sent.options.credentials,'omit');assert.equal(sent.options.redirect,'error');assert.equal(sent.options.headers.Authorization,`Bearer ${token}`);
 await assert.rejects(sshTargetsRequest(token,body,{fetcher:async()=>({ok:true,json:async()=>({target:{...target,address:'192.0.2.21'},saved:false})})}),/Mismatched/);
});
test('approval response must match reviewed digest and saved state',async()=>{const body={action:'approve',target_id:'new',config,review_digest:'c'.repeat(64)};await assert.rejects(sshTargetsRequest(token,body,{fetcher:async()=>({ok:true,json:async()=>({target,saved:true})})}),/Mismatched/)});
test('failed transport explicitly reports uncertain save',async()=>{await assert.rejects(sshTargetsRequest(token,null,{fetcher:async()=>{throw new Error('secret')}}),/Approval may have saved/)});
test('rejects malformed scope without guessing permissions',async()=>{await assert.rejects(sshTargetsRequest(token,null,{fetcher:async()=>({ok:true,json:async()=>({scopes:{key:{networks:['192.0.2.0/24'],users:['root'],ports:[0]}}})})}),/Unsupported/)});
