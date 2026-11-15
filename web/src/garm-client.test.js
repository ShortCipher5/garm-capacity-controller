import { test } from "node:test";
import assert from "node:assert/strict";
import { applyGarmChange, validateGarm } from "./garm-client.js";
import { ReadSession, SessionEnded } from "./client.js";
const read="a".repeat(64),write="c".repeat(64);
const settings={settings_available:true,writes_enabled:true,write_blocked:false,history:[],history_truncated:false,settings:{pools:[{id:"00000000-0000-4000-8000-000000000001",provider_name:"capacity",fingerprint:"f".repeat(64),enabled:true,priority:0,max_runners:4,min_idle_runners:1,generation:1,tags:["linux"]}],repositories:[],writes_enabled:true}};
const resources={cpu_millis:0,ram_mib:0,runners:0};
const overview={mode:"experimental-ledger",management_enabled:true,policy_revision:"f".repeat(64),experimental_provisioning_enabled:false,admission_ready:false,epoch:0,immutable_execution_plans:0,legacy_unplanned_reservations:0,legacy_source_preparations:0,observed_at:"2026-01-01T00:00:00Z",global:{budget:resources,allocated:resources},hosts:[],pools:[],events:[],states:{}};
test("Garm settings reject malformed or misleading types",()=>{
 assert.equal(validateGarm(settings),settings);
 for(const mutate of [(v)=>v.write_blocked="false",(v)=>delete v.settings_available,(v)=>v.settings.pools[0].priority=Number.MAX_SAFE_INTEGER+1,(v)=>v.settings.pools[0].tags=[1],(v)=>v.history={}]){
  const v=structuredClone(settings);mutate(v);assert.throws(()=>validateGarm(v),/unsupported/);
 }
});
test("Garm mutation is exact typed same-origin one-shot with no credential persistence",async()=>{
 let calls=0;
 const change={request_id:"11111111-1111-4111-8111-111111111111",target:settings.settings.pools[0].id,fingerprint:"f".repeat(64),reason:"test",pool:{priority:2}};
 const fetcher=async function(url,options){calls++;assert.equal(this,globalThis);assert.equal(url,"/admin/api/v1/garm/pool");assert.equal(options.method,"POST");assert.equal(options.headers.Authorization,`Bearer ${write}`);assert.equal(options.credentials,"omit");assert.equal(options.cache,"no-store");assert.equal(options.redirect,"error");assert.deepEqual(JSON.parse(options.body),change);return{ok:true,status:200,json:async()=>({recorded_confirmed:true})};};
 await applyGarmChange(write,"pool",change,{fetcher});assert.equal(calls,1);
 await assert.rejects(applyGarmChange("bad","pool",change,{fetcher}),/management token/);
 await assert.rejects(applyGarmChange(write,"delete",change,{fetcher}),/Unsupported/);assert.equal(calls,1);
});
test("Garm unknown outcome and rejection never silently retry",async()=>{
 for(const [status,message]of[[401,/read token cannot/],[409,/pending/],[400,/unsafe/],[503,/outcome unavailable/]]){
  let calls=0;await assert.rejects(applyGarmChange(write,"pool",{}, {fetcher:async()=>{calls++;return{status,ok:false};}}),message);assert.equal(calls,1);
 }
 let calls=0;await assert.rejects(applyGarmChange(write,"pool",{}, {fetcher:async()=>{calls++;throw Error("private upstream URL");}}),/outcome unknown/);assert.equal(calls,1);
});
test("remote read keeps token in session and disconnect fences ignored-abort response",async()=>{
 let resolve;
 const session=new ReadSession(async(url,options)=>{
  assert.equal(options.headers.Authorization,`Bearer ${read}`);assert.equal(options.credentials,"omit");assert.equal(options.redirect,"error");assert.equal(options.cache,"no-store");
  if(url.endsWith("overview"))return{ok:true,status:200,json:async()=>overview};
  return new Promise((done)=>resolve=done);
 });
 await session.connect(read);const pending=session.garmSnapshot();session.disconnect();resolve({ok:true,status:200,json:async()=>settings});await assert.rejects(pending,SessionEnded);await assert.rejects(session.garmSnapshot(),SessionEnded);
});
test("ordinary overview refresh does not supersede current remote discovery",async()=>{
 let resolve;
 const session=new ReadSession(async(url)=>url.endsWith("overview")?{ok:true,status:200,json:async()=>overview}:new Promise((done)=>resolve=done));
 await session.connect(read);const pending=session.garmSnapshot();await session.refresh();resolve({ok:true,status:200,json:async()=>settings});assert.deepEqual(await pending,settings);session.disconnect();
});

test("inventory validates safe read-only hashes and rejects contradictory identities",()=>{
 const value=structuredClone(settings);value.pool_inventory={pools:[{...value.settings.pools[0],fingerprint:"",configured:true,discovery_fingerprint:"a".repeat(64)}],missing_configured_pool_ids:[]};
 assert.equal(validateGarm(value),value);
 for(const mutate of [v=>v.pool_inventory.pools[0].fingerprint="f".repeat(64),v=>v.pool_inventory.pools.push(v.pool_inventory.pools[0]),v=>v.pool_inventory.missing_configured_pool_ids.push(v.pool_inventory.pools[0].id),v=>v.pool_inventory.pools[0].configured="true"]){const bad=structuredClone(value);mutate(bad);assert.throws(()=>validateGarm(bad),/unsupported/);}
});

test("zero-enrolled discovery cannot advertise editable state",()=>{
 const v=structuredClone(settings);v.discovery_only=true;v.settings_available=false;v.writes_enabled=false;v.settings.writes_enabled=false;v.settings.pools=[];v.settings.repositories=[];
 assert.equal(validateGarm(v),v);
 for(const mutate of [x=>x.writes_enabled=true,x=>x.settings_available=true,x=>x.settings.pools=settings.settings.pools]){const bad=structuredClone(v);mutate(bad);assert.throws(()=>validateGarm(bad),/unsupported/);}
});
