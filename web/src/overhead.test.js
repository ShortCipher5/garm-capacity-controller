import test from 'node:test';import assert from 'node:assert/strict';
import {validateOverhead,overheadSummary} from './overhead.js';
const r=(cpu=0,ram=0,disk=0,runners=0)=>({cpu_millis:cpu,ram_mib:ram,disk_mib:disk,runners});
test('overhead is an included subtotal and unavailable is not zero',()=>{const v={domain:r(100,200,300),artifact:r(0,0,400)};assert.equal(validateOverhead(v,r(1100,1200,1700,1)),v);assert.match(overheadSummary(v),/Already included/);assert.match(overheadSummary(),/do not assume it is zero/);});
test('overhead cannot invent runners or exceed canonical charges',()=>{for(const v of [{domain:r(0,0,0,1),artifact:r()},{domain:r(),artifact:r(1)},{domain:r(0,0,Number.MAX_SAFE_INTEGER),artifact:r(0,0,1)},{domain:r(-1),artifact:r()}])assert.throws(()=>validateOverhead(v,r(0,0,Number.MAX_SAFE_INTEGER)));});
