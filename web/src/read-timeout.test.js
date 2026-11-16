import {test} from 'node:test';
import assert from 'node:assert/strict';
import {ReadSession, SessionEnded} from './client.js';

const resource={cpu_millis:0,ram_mib:0,runners:0};
const overview={mode:'experimental-ledger',management_enabled:false,policy_revision:'f'.repeat(64),experimental_provisioning_enabled:false,admission_ready:false,epoch:0,immutable_execution_plans:0,legacy_unplanned_reservations:0,legacy_source_preparations:0,observed_at:'2026-01-01T00:00:00Z',global:{budget:resource,allocated:resource},hosts:[],pools:[],states:{},events:[]};
const discovery={mode:'read-only-onboarding-preflight',probe_enabled:false,enrollment_supported:false,capacity_qualified:false,profiles:[]};
const garm={settings_available:true,writes_enabled:false,write_blocked:true,history:[],history_truncated:false,settings:{pools:[],repositories:[],writes_enabled:false}};

for(const method of ['refresh','onboardingSnapshot','garmSnapshot','entityInventorySnapshot']){
 for(const phase of ['headers','body']){
  test(`${method} rejects expired ${phase} even when transport ignores abort`,async t=>{
   t.mock.timers.enable({apis:['setTimeout']});
   let pending=false,calls=0,release,started,signal;
   const reached=new Promise(r=>started=r);
   const delay=new Promise(r=>release=r);
   const value=method==='onboardingSnapshot'?discovery:method==='refresh'?overview:garm;
   const session=new ReadSession(async(_url,options)=>{
    calls++;if(!pending)return {ok:true,status:200,json:async()=>overview};
    signal=options.signal;
    if(phase==='headers'){started();await delay;}
    return {ok:true,status:200,json:async()=>{if(phase==='body'){started();await delay;}return value;}};
   });
   await session.connect('a'.repeat(64));pending=true;
   const work=session[method]();const rejected=assert.rejects(work,error=>method==='entityInventorySnapshot'?error instanceof SessionEnded:!(error instanceof SessionEnded));
   await reached;t.mock.timers.tick(15000);assert.equal(signal.aborted,true);release();
   await rejected;assert.equal(calls,2);session.disconnect();
  });
 }
}
