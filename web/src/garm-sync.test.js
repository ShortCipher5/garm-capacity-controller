import test from 'node:test';
import assert from 'node:assert/strict';
import {garmSettingsChanged} from './garm-sync.js';
const snapshot=()=>({settings_available:true,writes_enabled:true,write_blocked:false,settings:{writes_enabled:true,pools:[{id:'a',fingerprint:'1'},{id:'b',fingerprint:'2'}],repositories:[{id:'r',fingerprint:'3'}]}});
test('external pool/repository edits and deletion are drift',()=>{
  for(const change of [s=>s.settings.pools[0].fingerprint='new',s=>s.settings.repositories[0].fingerprint='new',s=>s.settings.pools.pop(),s=>s.write_blocked=true,s=>s.settings_available=false,s=>s.writes_enabled=false]){const next=snapshot();change(next);assert.equal(garmSettingsChanged(snapshot(),next),true);}
});
test('reordering and history alone do not discard drafts',()=>{const next=snapshot();next.settings.pools.reverse();next.history=[{state:'confirmed'}];assert.equal(garmSettingsChanged(snapshot(),next),false);});
