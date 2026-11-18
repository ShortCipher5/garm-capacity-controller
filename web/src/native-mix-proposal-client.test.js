import test from 'node:test';import assert from 'node:assert/strict';
import {readMixProposal,validateMixProposal,mixProposalFresh} from './native-mix-proposal-client.js';
const hash='a'.repeat(64),token='b'.repeat(64),binding={host:'host-a'},target={target_id:'server',binding};
const saved={state:'saved_configuration_not_applied',selection_digest:hash,selection:{binding,physical_capacity_qualified:false,saved_at:new Date(Date.now()-1000).toISOString(),expires_at:new Date(Date.now()+600000).toISOString(),partition:{Guests:{CPUMilli:4000,MemoryBytes:8192*1048576}}}};
const proposal={host:'host-a',selection_digest:hash,selection_expires_at:saved.selection.expires_at,entries:[{name:'small',cpu_millis:1000,ram_mib:2048,disk_mib:4096,count:2},{name:'medium',cpu_millis:2000,ram_mib:4096,disk_mib:4096,count:1}],configured_guest_cpu_millis:4000,configured_guest_ram_mib:8192,disk_fit_verified:false,state:'proposed_from_saved_configuration_not_applied'};
test('proposal is exact saved configuration with explicit disk, never capacity authority',()=>{
 assert.deepEqual(validateMixProposal({target_id:'server',proposal},target,saved,4096),proposal);
 for(const patch of [{host:'other'},{selection_digest:token},{disk_fit_verified:true},{configured_guest_cpu_millis:9999},{selection_expires_at:new Date(Date.now()-1).toISOString()},{entries:[{...proposal.entries[0],disk_mib:1024}]},{entries:[{...proposal.entries[0],count:5}]}])assert.throws(()=>validateMixProposal({target_id:'server',proposal:{...proposal,...patch}},target,saved,4096));
 assert.equal(mixProposalFresh(proposal,Date.parse(proposal.selection_expires_at)),false);
});
test('actual proposal request reads fixed source only and never sends save confirmation',async()=>{
 const result=await readMixProposal(token,target,saved,4096,{fetcher:async(path,opts)=>{assert.equal(path,'/admin/api/v1/native-planning');assert.deepEqual(JSON.parse(opts.body),{action:'mix-proposal',target_id:'server',selection_digest:hash,disk_mib:4096});assert.equal(opts.credentials,'omit');assert.equal(opts.redirect,'error');return{status:200,json:async()=>({target_id:'server',proposal})};}});assert.deepEqual(result,proposal);
 let count=0;await assert.rejects(readMixProposal(token,target,saved,0,{fetcher:async()=>count++}));assert.equal(count,0);
 await assert.rejects(readMixProposal(token,target,saved,4096,{fetcher:async()=>{count++;throw Error('lost');}}),/No profiles or counts were saved/);assert.equal(count,1);
});
test('late aborted response cannot become a transferable proposal',async()=>{const c=new AbortController();await assert.rejects(readMixProposal(token,target,saved,4096,{signal:c.signal,fetcher:async()=>({status:200,json:async()=>{c.abort();return{target_id:'server',proposal};}})}),/unavailable/);});
