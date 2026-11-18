import test from 'node:test';import assert from 'node:assert/strict';import {validateCapacityStates} from './capacity-states.js';
const size={cpu_millis:2,ram_mib:3,disk_mib:4,runners:1};
const view=()=>({source:'charged-ledger',grants_available:false,observed_at:'2026-01-01T00:00:00Z',total:{...size},rows:[{state:'unknown',resources:{...size}}]});
test('accepts charged state only with internally consistent totals',()=>assert.equal(validateCapacityStates(view()).rows[0].state,'unknown'));
test('rejects invalid grant availability or inconsistent totals',()=>{for(const mutate of [v=>v.grants_available="true",v=>v.total.runners=2,v=>v.rows.push(v.rows[0]),v=>v.rows[0].resources.disk_mib=-1]){const v=view();mutate(v);assert.throws(()=>validateCapacityStates(v));}});

test('rejects unsupported completion states',()=>{for(const state of ['released','completed','busy','made_up']){const v=view();v.rows[0].state=state;assert.throws(()=>validateCapacityStates(v));}});

test('accepts accounted grants only when reported available',()=>{const v=view();v.rows[0].state='granted';assert.throws(()=>validateCapacityStates(v));v.grants_available=true;assert.equal(validateCapacityStates(v).rows[0].state,'granted');});
