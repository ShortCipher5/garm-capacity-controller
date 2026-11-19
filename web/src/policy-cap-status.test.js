import test from 'node:test';import assert from 'node:assert/strict';import {validatePolicyCapStatus,capEventTime} from './policy-cap-status.js';
const value={state:'blocked',policy_revision:7,confirmed:1,skipped:2,last_attempt:'2026-11-18T10:00:00Z',last_success:'2026-11-18T09:00:00Z',last_request_id:'11111111-1111-4111-8111-111111111111'};
test('blocked cap status preserves earlier success without reinterpreting it as ready',()=>{assert.equal(validatePolicyCapStatus(value).state,'blocked');assert.equal(capEventTime('0001-01-01T00:00:00Z'),'not recorded');for(const delta of [{state:'ready'},{confirmed:-1},{confirmed:11},{skipped:1001},{policy_revision:'7'},{last_request_id:'retry-me'},{last_success:'invalid'}])assert.throws(()=>validatePolicyCapStatus({...value,...delta}));});

test('pool outcomes are bounded historical diagnostics', () => {
 const row={pool_id:'11111111-1111-4111-8111-111111111111',outcome:'not_examined'};
 assert.doesNotThrow(()=>validatePolicyCapStatus({...value,pool_results:[row]}));
 for(const rows of [null,[row,row],[{...row,outcome:['not_examined']}],[{...row,outcome:'capacity_released'}],[{...row,pool_id:'invalid'}],[{...row,token:'unexpected'}],Array.from({length:1001},()=>row)]) {
  assert.throws(()=>validatePolicyCapStatus({...value,pool_results:rows}));
 }
});
