import test from 'node:test';
import assert from 'node:assert/strict';
import {preserveDraftWithLatestInventory, retainGarmDraftConflict} from './garm-observation.js';
import {garmSettingsChanged} from './garm-sync.js';
const snapshot=()=>({settings_available:true,writes_enabled:true,write_blocked:false,settings:{writes_enabled:true,pools:[{id:'p',fingerprint:'old'}],repositories:[]},pool_inventory:{pools:[{id:'original'}]}});
test('latched conflict retains review barrier after settings revert while refreshing inventory',()=>{
 const previous=snapshot(), latest=snapshot();latest.pool_inventory={pools:[{id:'new'}]};
 assert.equal(garmSettingsChanged(previous,latest),false);
 assert.equal(retainGarmDraftConflict(previous,latest,true,false,garmSettingsChanged),true);
 const result=preserveDraftWithLatestInventory(previous,latest);
 assert.equal(result.settings,previous.settings);assert.equal(result.pool_inventory,latest.pool_inventory);
 assert.equal(previous.pool_inventory.pools[0].id,'original');
});
test('missing latest inventory removes stale observation without fabricating an empty collection',()=>{
 const previous=snapshot(),latest=snapshot();delete latest.pool_inventory;
 const result=preserveDraftWithLatestInventory(previous,latest);
 assert.equal(result.pool_inventory,undefined);assert.equal(result.settings,previous.settings);
});
test('unlatched unchanged or clean draft does not invent conflict; dirty changed settings do',()=>{
 const previous=snapshot(), latest=snapshot();
 assert.equal(retainGarmDraftConflict(previous,latest,false,true,garmSettingsChanged),false);
 latest.settings.pools[0].fingerprint='new';
 assert.equal(retainGarmDraftConflict(previous,latest,false,false,garmSettingsChanged),false);
 assert.equal(retainGarmDraftConflict(previous,latest,false,true,garmSettingsChanged),true);
});
test('a complete empty observation replaces old pools without clearing the edit barrier',()=>{
 const previous=snapshot(),latest=snapshot();latest.pool_inventory={pools:[],missing_configured_pool_ids:['p']};
 assert.equal(retainGarmDraftConflict(previous,latest,true,true,garmSettingsChanged),true);
 assert.deepEqual(preserveDraftWithLatestInventory(previous,latest).pool_inventory,latest.pool_inventory);
 assert.equal(previous.pool_inventory.pools.length,1);
});

test('draft freeze does not hide a new pending or unknown journal observation',()=>{
 const previous=snapshot(),latest=snapshot();
 latest.settings.pools[0].fingerprint='changed';latest.write_blocked=true;
 latest.pending={request_id:'request-2',kind:'pool',target:'p'};
 latest.history=[{request_id:'request-2',state:'unknown'}];latest.history_truncated=true;
 const result=preserveDraftWithLatestInventory(previous,latest);
 assert.equal(result.settings,previous.settings);
 assert.equal(result.write_blocked,true);assert.equal(result.pending,latest.pending);
 assert.equal(result.history,latest.history);assert.equal(result.history_truncated,true);
 assert.equal(retainGarmDraftConflict(result,latest,true,false,garmSettingsChanged),true);
 assert.equal(previous.write_blocked,false);
});
test('new journal observation can remove stale pending without clearing conflict or changing draft',()=>{
 const previous=snapshot(),latest=snapshot();
 previous.pending={request_id:'old'};previous.history=[{state:'pending'}];previous.history_truncated=true;
 latest.history=[];latest.history_truncated=false;
 const result=preserveDraftWithLatestInventory(previous,latest);
 assert.equal(result.pending,undefined);assert.deepEqual(result.history,[]);
 assert.equal(result.history_truncated,false);assert.equal(result.settings,previous.settings);
 assert.equal(retainGarmDraftConflict(result,latest,true,false,garmSettingsChanged),true);
});
