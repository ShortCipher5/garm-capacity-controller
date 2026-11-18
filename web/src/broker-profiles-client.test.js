import test from 'node:test';
import assert from 'node:assert/strict';
import {validatePoolMappings,registerPoolMapping} from './broker-profiles-client.js';
const resources={cpu_millis:1000,ram_mib:1024,disk_mib:4096,runners:1};
const input={recipe:'small',pool:'pool-a',recipe_revision:'f'.repeat(64),size_profile:'small',expected_sequence:3,resources};
const token='a'.repeat(64);
test('strict mappings reject unavailable and ambiguous response',()=>{
 assert.deepEqual(validatePoolMappings({configuration_only:true,writes_enabled:false,profiles:[],recipes:[]}).profiles,[]);
 for(const v of [null,{}, {configuration_only:false,writes_enabled:true,profiles:[],recipes:[]},{configuration_only:true,writes_enabled:true,profiles:[],recipes:[{id:'x',pool:'p'},{id:'x',pool:'p'}]}])assert.throws(()=>validatePoolMappings(v));
});
test('registration uses scoped same-origin POST and validates exact captured size',async()=>{
 let calls=0;await registerPoolMapping(token,input,{fetcher:async(url,options)=>{calls++;assert.equal(url,'/admin/api/v1/broker-profiles');assert.equal(options.method,'POST');assert.equal(options.credentials,'omit');assert.equal(options.cache,'no-store');assert.equal(options.redirect,'error');const {pool,...body}=input;assert.deepEqual(JSON.parse(options.body),body);return {ok:true,json:async()=>({saved:true,configuration_only:true,profile:{sequence:4,pool:'pool-a',size_profile:'small',resources}})};}});assert.equal(calls,1);
 await assert.rejects(registerPoolMapping(token,input,{fetcher:async()=>({ok:true,json:async()=>({saved:true,configuration_only:true,profile:{sequence:4,pool:'pool-a',size_profile:'small',resources:{...resources,ram_mib:2048}}})})}));
});
test('no invalid dispatch or automatic retry after unknown outcome',async()=>{
 let calls=0;const fetcher=async()=>{calls++;throw Error('network');};
 await assert.rejects(registerPoolMapping('bad',input,{fetcher}));assert.equal(calls,0);
 await assert.rejects(registerPoolMapping(token,input,{fetcher}),/outcome unknown/);assert.equal(calls,1);
 await assert.rejects(registerPoolMapping(token,{...input,resources:{...resources,disk_mib:0}},{fetcher}));assert.equal(calls,1);
});
import {ReadSession,SessionEnded} from './client.js';
const resource={cpu_millis:0,ram_mib:0,runners:0};
const overview={mode:'experimental-ledger',management_enabled:false,policy_revision:'f'.repeat(64),experimental_provisioning_enabled:false,admission_ready:false,epoch:0,immutable_execution_plans:0,legacy_unplanned_reservations:0,legacy_source_preparations:0,observed_at:'2026-01-01T00:00:00Z',global:{budget:resource,allocated:resource},hosts:[],pools:[],states:{},events:[]};
test('mapping reader fences body resolving after disconnect',async()=>{
 let release,started;const body=new Promise(r=>release=r),reached=new Promise(r=>started=r);
 const session=new ReadSession(async url=>url.endsWith('/overview')?{ok:true,status:200,json:async()=>overview}:{ok:true,status:200,json:async()=>{started();return body;}});
 await session.connect(token);const pending=session.poolMappingsSnapshot();const rejected=assert.rejects(pending,SessionEnded);await reached;session.disconnect();release({configuration_only:true,writes_enabled:false,profiles:[],recipes:[]});await rejected;
});
