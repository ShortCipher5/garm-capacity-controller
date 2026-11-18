import test from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {createServer} from 'vite';
import {readBudget,budgetWarnings} from './budget-review.js';
const budget={cpu_millis:2000,ram_mib:4096,disk_mib:20480,runners:2};
test('all four dimensions must be explicit; disk cannot silently become unconstrained',()=>{
 const form=new FormData();for(const [key,value] of Object.entries(budget))form.set(key,String(value));assert.deepEqual(readBudget(form),budget);
 for(const value of [null,'','-1','1.5','9007199254740992']){if(value===null)form.delete('disk_mib');else form.set('disk_mib',value);assert.throws(()=>readBudget(form));}
 form.set('disk_mib','0');assert.equal(readBudget(form).disk_mib,0);
});
test('headroom warnings use detected totals and never promise free capacity',()=>{
 const host={detected:{cpu_millis:8000,ram_mib:24576,disk_mib:200000},allocated:budget};
 const warnings=budgetWarnings(host,{...budget,ram_mib:23552},true).join(' ');assert.match(warnings,/only 1024 remains/);assert.match(warnings,/last-discovered/);
 assert.doesNotMatch(budgetWarnings(host,budget,true).join(' '),/only .* remains/);
 assert.match(budgetWarnings(host,{...budget,ram_mib:99999},true).join(' '),/exceeds/);
 assert.match(budgetWarnings({},budget,true).join(' '),/unknown/);
});
test('lowering budgets retains charges; zero disk is explicitly unconstrained',()=>{
 const selected={allocated:budget};assert.match(budgetWarnings(selected,{...budget,ram_mib:1}).join(' '),/4096 remains charged/);
 const warnings=budgetWarnings(selected,{...budget,disk_mib:0}).join(' ');assert.match(warnings,/disables the disk ceiling/);assert.doesNotMatch(warnings,/20480 remains charged above/);
});
test('allocation cards show disk and avoid false overbudget or availability for unconstrained disk',async()=>{
 const server=await createServer({server:{middlewareMode:true,hmr:false},appType:'custom'});
 try{const {Usage}=await server.ssrLoadModule('/src/usage-bars.jsx');for(const metric of [false,true]){const html=renderToStaticMarkup(React.createElement(Usage,{allocation:{allocated:budget,budget:{...budget,disk_mib:0}},metric}));assert.match(html,/Disk allocation/);assert.match(html,/No disk ceiling configured/);assert.doesNotMatch(html,/over budget|Over declared/);}}finally{await server.close();}
});
