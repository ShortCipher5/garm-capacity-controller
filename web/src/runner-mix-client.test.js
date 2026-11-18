import {test} from 'node:test';
import assert from 'node:assert/strict';
import {validateMix,validateMixView,saveRunnerMix} from './runner-mix-client.js';
import {ReadSession,SessionEnded} from './client.js';
const entry={name:'small',cpu_millis:1000,ram_mib:2048,disk_mib:4096,count:0};
const view={sequence:5,mix:{host:'host-1',revision:2,entries:[entry]},profiles_match:true};
const token='a'.repeat(64),digest=token,now='2026-11-18T10:00:00Z';
const overview={mode:'experimental-ledger',management_enabled:false,policy_revision:digest,experimental_provisioning_enabled:false,admission_ready:false,epoch:0,immutable_execution_plans:0,legacy_unplanned_reservations:0,legacy_source_preparations:0,observed_at:now,global:{budget:{cpu_millis:0,ram_mib:0,runners:0},allocated:{cpu_millis:0,ram_mib:0,runners:0}},hosts:[],pools:[],states:{},events:[]};
test('validates saved counts without treating them as mapped capacity',()=>{
 assert.deepEqual(validateMixView(view,'host-1'),view);
 assert.deepEqual(validateMixView({sequence:0,mix:{host:'host-1',revision:0,entries:[]},profiles_match:false},'host-1').mix.entries,[]);
 for(const change of [{disk_mib:0},{count:-1},{count:1000001},{cpu_millis:1500},{ram_mib:0}])assert.throws(()=>validateMix({...view.mix,entries:[{...entry,...change}]}));
 assert.throws(()=>validateMix({...view.mix,entries:[entry,entry]}));
 assert.throws(()=>validateMixView(view,'other'));
 assert.throws(()=>validateMix({...view.mix,entries:[{...entry,ram_mib:Number.MAX_SAFE_INTEGER,count:2}]}));
});
test('posts exact CAS and checks committed revision and target',async()=>{
 let seen;
 const receipt={host:'host-1',revision:3,state:'saved_profiles_and_desired_mix_not_mapped'};
 await saveRunnerMix(token,view,{fetcher:async(path,opts)=>{seen=JSON.parse(opts.body);assert.equal(path,'/admin/api/v1/runner-mix');assert.equal(opts.credentials,'omit');assert.equal(opts.redirect,'error');return{status:200,json:async()=>receipt};}});
 assert.deepEqual(seen,{expected_sequence:5,mix:view.mix});
 for(const bad of [null,[],{...receipt,host:'other'},{...receipt,revision:4},{...receipt,state:'running'}])await assert.rejects(saveRunnerMix(token,view,{fetcher:async()=>({status:200,json:async()=>bad})}),/uncertain/);
});
test('uncertain saves never replay and conflicts demand fresh read',async()=>{
 let calls=0;await assert.rejects(saveRunnerMix(token,view,{fetcher:async()=>{calls++;throw new Error('lost');}}),/Reload/);assert.equal(calls,1);
 await assert.rejects(saveRunnerMix(token,view,{fetcher:async()=>({status:409})}),/changed/);
 await assert.rejects(saveRunnerMix(token,view,{fetcher:async()=>({status:200,json:async()=>{throw new Error('bad JSON');}})}),/uncertain/);
});
test('actual ReadSession binds host route and rejects disconnected late JSON',async()=>{
 let release;const session=new ReadSession(async(path,opts)=>{if(path.endsWith('/overview'))return {ok:true,json:async()=>overview};assert.equal(path,'/admin/api/v1/runner-mix/host-1');assert.equal(opts.method,'GET');return{ok:true,status:200,json:()=>new Promise(r=>{release=r;})};});await session.connect(token);
 const pending=session.runnerMixSnapshot('host-1');await new Promise(r=>setImmediate(r));session.disconnect();release(view);await assert.rejects(pending,SessionEnded);
});
