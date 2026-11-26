import {test} from "node:test";
import assert from "node:assert/strict";
import {ReadSession,SessionEnded} from "./client.js";
import {createEntityInventoryReader} from "./entity-inventory-client.js";
const id="11111111-1111-4111-8111-111111111111",owner="22222222-2222-4222-8222-222222222222";
const pool={id,owner_kind:"organization",owner_id:owner,provider_name:"test",fingerprint:"",discovery_fingerprint:"f".repeat(64),configured:false,enabled:true,priority:1,max_runners:2,min_idle_runners:0,generation:1,tags:[]};
const snapshot=()=>({settings_available:false,writes_enabled:false,write_blocked:false,history:[],history_truncated:false,settings:{writes_enabled:false,pools:[],repositories:[]},pool_inventory:{pools:[pool],missing_configured_pool_ids:[]}});
const resource={cpu_millis:0,ram_mib:0,runners:0};
const overview={mode:"experimental-ledger",management_enabled:false,policy_revision:"f".repeat(64),experimental_provisioning_enabled:false,admission_ready:false,epoch:0,immutable_execution_plans:0,legacy_unplanned_reservations:0,legacy_source_preparations:0,observed_at:"2026-01-01T00:00:00Z",global:{budget:resource,allocated:resource},hosts:[],pools:[],events:[],states:{}};
const sessionOf=fn=>new ReadSession((url,o)=>url.endsWith("/overview")?Promise.resolve(response(overview)):fn(url,o));
const response=v=>({ok:true,status:200,json:async()=>v});
test("authenticated provider inventory projects fresh exact owner without edit grants",async()=>{
 let calls=0;const session=sessionOf(async(url,o)=>{calls++;assert.equal(url,"/admin/api/v1/garm");assert.equal(o.method,"GET");assert.equal(o.headers.Authorization,`Bearer ${"a".repeat(64)}`);assert.equal(o.credentials,"omit");assert.equal(o.redirect,"error");assert.equal(o.cache,"no-store");assert.equal(o.body,undefined);return response(snapshot())});await session.connect("a".repeat(64));
 const reader=createEntityInventoryReader(o=>session.entityInventorySnapshot(o));assert.deepEqual(await reader.readEntities(),{available:true,entities:[{type:"organization",id:owner,pool_count:1}]});const result=await reader.readEntityPools({type:"organization",id:owner});assert.deepEqual(result,{available:true,pools:[{id,enabled:true,priority:1,max_runners:2,min_idle_runners:0}]});assert.equal(calls,2);await assert.rejects(reader.readEntityPools({type:"repository",id:owner}));assert.equal(calls,3);
});
test("missing inventory or unknown ownership is unavailable rather than partial or enrolled",async()=>{
 for(const change of [v=>delete v.pool_inventory,v=>v.pool_inventory.pools[0]={...pool,owner_kind:"unknown",owner_id:undefined},v=>v.pool_inventory.pools[0]={...pool,owner_id:"00000000-0000-0000-0000-000000000000"},v=>v.pool_inventory.pools.push(pool)]){const v=snapshot();change(v);const reader=createEntityInventoryReader(async()=>v);await assert.rejects(reader.readEntities());}
});
test("cancel and disconnect fence ignored-abort entity responses without disrupting settings read",async()=>{
 let done;const session=sessionOf(async()=>new Promise(r=>done=r));await session.connect("a".repeat(64));const abort=new AbortController();const pending=session.entityInventorySnapshot({signal:abort.signal});abort.abort();done(response(snapshot()));await assert.rejects(pending,SessionEnded);
 const another=session.entityInventorySnapshot();session.disconnect();done(response(snapshot()));await assert.rejects(another,SessionEnded);
 let entitySignal;const separate=sessionOf(async(_url,o)=>{if(!entitySignal){entitySignal=o.signal;return new Promise(r=>done=r)}return response(snapshot())});await separate.connect("a".repeat(64));const entity=separate.entityInventorySnapshot();await separate.garmSnapshot();assert.equal(entitySignal.aborted,false);done(response(snapshot()));await entity;
});
test("entity auth rejection ends session and error details are redacted",async()=>{
 const s=sessionOf(async()=>({ok:false,status:401}));await s.connect("a".repeat(64));await assert.rejects(s.entityInventorySnapshot(),/authorization ended/);await assert.rejects(s.garmSnapshot(),SessionEnded);
 const bad=sessionOf(async()=>{throw Error("private-token")});await bad.connect("a".repeat(64));await assert.rejects(bad.entityInventorySnapshot(),e=>!e.message.includes("private-token"));
});

test("a configured repository with no observed pools is still inspectable",async()=>{
 const v=snapshot();
 v.settings_available=true;
 v.settings.repositories=[{id:owner,owner:"example",name:"app",pool_balancing_type:"roundrobin",fingerprint:"f".repeat(64)}];
 const reader=createEntityInventoryReader(async()=>v);
 const found=await reader.readEntities();
 assert.deepEqual(found.entities,[{type:"organization",id:owner,pool_count:1},{type:"repository",id:owner,pool_count:0,configured:true}]);
 assert.deepEqual(await reader.readEntityPools({type:"repository",id:owner}),{available:true,pools:[]});
 v.settings_available=false;
 await assert.rejects(reader.readEntityPools({type:"repository",id:owner}));
});
