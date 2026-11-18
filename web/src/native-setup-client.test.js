import test from 'node:test';import assert from 'node:assert/strict';
import {validateCollectionView,validateCollectionOperation,startCollection,newCollectionRequestID,validateEnrollmentView,enrollNativeDomains} from './native-setup-client.js';
import {ReadSession,SessionEnded} from './client.js';
const digest='a'.repeat(64),id='b'.repeat(32),now='2026-11-18T10:00:00Z';
const choice={id:'server-1',revision:digest,targets:[{host:'server one',binding_sequence:4,kind:'both'}]};
const op=(state='pending')=>({request_id:id,recipe:choice.id,recipe_revision:digest,state,started_at:now,finished_at:state==='pending'||state==='unknown'?'0001-01-01T00:00:00Z':'2026-11-18T10:00:01Z',results:state==='complete'?[{binding_sequence:4,domain_sequence:8,artifact_sequence:7}]:[]});
const view=()=>({recipes:[structuredClone(choice)],operations:[op()],running:true,can_start:false,blocked_reason:'collection_running',collection_enabled:true,accounting_only:true});
test('collection status distinguishes active pending, unknown and complete receipts',()=>{assert.equal(validateCollectionView(view()).running,true);assert.equal(validateCollectionOperation(op('unknown'),choice).state,'unknown');assert.equal(validateCollectionOperation(op('complete'),choice).results.length,1);assert.match(newCollectionRequestID(),/^[a-f0-9]{32}$/);});
test('incomplete or foreign scope cannot be rendered complete',()=>{for(const change of [v=>v.results=[],v=>v.results[0].binding_sequence=9,v=>delete v.results[0].artifact_sequence,v=>v.recipe_revision='c'.repeat(64),v=>v.finished_at='0001-01-01T00:00:00Z']){const v=op('complete');change(v);assert.throws(()=>validateCollectionOperation(v,choice));}const v=view();v.running=false;assert.throws(()=>validateCollectionView(v));});
test('collection sends only fixed recipe/revision/new request and never automatically retries',async()=>{
 const calls=[];const result=await startCollection(digest,{...choice,endpoint:'https://not-sent.invalid'},id,{fetcher:async(url,o)=>{calls.push([url,o]);return{status:202,ok:true,json:async()=>op()};}});
 assert.equal(result.state,'pending');assert.equal(calls[0][0],'/admin/api/v1/overhead-refresh');assert.deepEqual(JSON.parse(calls[0][1].body),{recipe:'server-1',recipe_revision:digest,request_id:id});assert.equal(calls[0][1].credentials,'omit');
 let attempts=0;await assert.rejects(startCollection(digest,choice,id,{fetcher:async()=>{attempts++;throw Error('lost');}}),/outcome unknown/);assert.equal(attempts,1);
 await assert.rejects(startCollection(digest,choice,id,{fetcher:async()=>({ok:true,status:202,json:async()=>({...op(),request_id:'d'.repeat(32)})})}),/does not match/);
});
const enrollment=()=>({recipes:[{id:'native',revision:digest,hosts:['server one'],mode:'initial'}],enrolled:[],ledger_sequence:3,writes_enabled:true,readiness_authorized:false});
test('enrollment review rejects incoherent sequence and readiness claims',()=>{assert.equal(validateEnrollmentView(enrollment()).recipes[0].mode,'initial');for(const change of [v=>v.readiness_authorized=true,v=>v.enrolled=[{host:'server one',domain_id:digest,sequence:4}],v=>v.recipes[0].mode='auto']){const v=enrollment();change(v);assert.throws(()=>validateEnrollmentView(v));}});
test('enrollment response correlation and partial accounting stay explicit',async()=>{
 const c=enrollment().recipes[0],calls=[];
 const receipt={recipe:c.id,recipe_revision:digest,expected_sequence:3,confirmed_domains:1,partial:false,configuration_only:true,readiness_authorized:false};
 const v=await enrollNativeDomains(digest,c,3,{fetcher:async(url,o)=>{calls.push([url,o]);return{ok:true,status:200,json:async()=>receipt};}});
 assert.equal(v.outcome,'confirmed');assert.deepEqual(JSON.parse(calls[0][1].body),{recipe:'native',recipe_revision:digest,expected_sequence:3});
 const partial=await enrollNativeDomains(digest,c,3,{fetcher:async()=>({ok:false,status:503,json:async()=>({...receipt,confirmed_domains:0,partial:true})})});assert.equal(partial.outcome,'partial_or_failed');
 await assert.rejects(enrollNativeDomains(digest,c,3,{fetcher:async()=>({ok:true,status:200,json:async()=>({...receipt,expected_sequence:4})})}),/could not be correlated/);
});
const overview={mode:'experimental-ledger',management_enabled:false,policy_revision:digest,experimental_provisioning_enabled:false,admission_ready:false,epoch:0,immutable_execution_plans:0,legacy_unplanned_reservations:0,legacy_source_preparations:0,observed_at:now,global:{budget:{cpu_millis:0,ram_mib:0,runners:0},allocated:{cpu_millis:0,ram_mib:0,runners:0}},hosts:[],pools:[],states:{},events:[]};
test('setup reads are fenced against late responses after disconnect',async()=>{
 for(const method of ['overheadRefreshSnapshot','nativeEnrollmentSnapshot']){
 let finish;const calls=[];const session=new ReadSession(async(url,o)=>{calls.push([url,o]);if(url.endsWith('/overview'))return{ok:true,json:async()=>overview};return new Promise(resolve=>finish=resolve);});
 await session.connect(digest);const pending=session[method]();session.disconnect();finish({ok:true,json:async()=>method==='overheadRefreshSnapshot'?view():enrollment()});await assert.rejects(pending,SessionEnded);assert.equal(calls[1][1].method,'GET');assert.equal(calls[1][1].signal.aborted,true);
 }
});

test("enrollment rejects zero ledger revision before any write",async()=>{assert.throws(()=>validateEnrollmentView({...enrollment(),ledger_sequence:0}));let called=false;await assert.rejects(enrollNativeDomains(digest,enrollment().recipes[0],0,{fetcher:async()=>{called=true;}}));assert.equal(called,false);});

test("collection rejects impossible receipt states and non-contract success status",async()=>{assert.throws(()=>validateCollectionOperation(op("partial"),choice));for(const state of ["pending","failed"])assert.throws(()=>validateCollectionOperation({...op(state),results:op("complete").results},choice));await assert.rejects(startCollection(digest,choice,id,{fetcher:async()=>({ok:true,status:200,json:async()=>op()})}),/unconfirmed/);});
