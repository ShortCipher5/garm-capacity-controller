import assert from "node:assert/strict";
import test from "node:test";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { createServer } from "vite";

test("Garm discovery renders honest separate summaries, never editable children", async () => {
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom" });
  try {
    const { canEditGarm, GarmSettingsPresentation } = await server.ssrLoadModule("/src/garm-discovery.jsx");
    const snapshot = { settings_available: true, writes_enabled: false, settings: { writes_enabled: true,
      pools: [{ id: "pool-only-id", provider_name: "capacity", enabled: false, max_runners: 4, min_idle_runners: 0, priority: 2, generation: 3, tags: ["linux", "<script>not markup</script>"] }],
      repositories: [{ id: "unrelated-repository-id", owner: "example", name: "app", pool_balancing_type: "pack" }] } };
    const edit = React.createElement("form", {}, React.createElement("input", { name: "reason" }), React.createElement("button", {}, "Review Garm change"));
    const render = (value) => renderToStaticMarkup(React.createElement(GarmSettingsPresentation, { snapshot: value }, edit));
    const html = render(snapshot);
    assert.match(html, /Pool pool-only-id/);
    assert.match(html, /Provider: capacity/);
    assert.match(html, /Disabled, not drained/);
    assert.match(html, /Maximum runners: 4 · Minimum idle runners: 0/);
    assert.match(html, /Priority: 2 · Generation: 3/);
    assert.match(html, /linux, &lt;script&gt;not markup&lt;\/script&gt;/);
    assert.match(html, /example\/app/);
    assert.match(html, /Repository ID: unrelated-repository-id/);
    assert.match(html, /Repository balancing: Pack/);
    assert.doesNotMatch(html, /<form|<input|<textarea|<select|<button|<script>/);
    assert.equal(canEditGarm(snapshot), false);
    assert.equal(render({ ...snapshot, settings_available: false }), "");
    assert.match(render({ ...snapshot, writes_enabled: true }), /<form/);
    const remoteDisabled = { ...snapshot, writes_enabled: true, settings: { ...snapshot.settings, writes_enabled: false } };
    assert.equal(canEditGarm(remoteDisabled), false);
    assert.doesNotMatch(render(remoteDisabled), /<form|<input/);
    const empty = render({ ...snapshot, settings: { ...snapshot.settings, pools: [], repositories: [] } });
    assert.match(empty, /No pools returned in this configured discovery scope/);
    assert.match(empty, /No repositories returned in this configured discovery scope/);
    assert.doesNotMatch(empty, /No pools exist|No repositories exist/);
  } finally { await server.close(); }
});

test("inventory displays external and missing pools without edit or recreation controls",async()=>{
 const server=await createServer({server:{middlewareMode:true,hmr:false},appType:"custom"});
 try{const {GarmPoolInventory}=await server.ssrLoadModule("/src/garm-discovery.jsx");
 const html=renderToStaticMarkup(React.createElement(GarmPoolInventory,{inventory:{pools:[{id:"existing",provider_name:"capacity",configured:false,enabled:true,max_runners:4,min_idle_runners:1,priority:2,tags:[]}],missing_configured_pool_ids:["missing"]}}));
 assert.match(html,/existing/);assert.match(html,/outside the management allowlist; read-only/);assert.match(html,/missing/);assert.match(html,/Absence is not proof of deletion/);assert.doesNotMatch(html,/<form|type="submit"|Apply reviewed|Review Garm change/);
 }finally{await server.close();}
});

test("inventory shows exact upstream ownership and escaped future-runner tags, never edit controls",async()=>{
 const server=await createServer({server:{middlewareMode:true,hmr:false},appType:"custom"});
 try {
  const {GarmPoolInventory}=await server.ssrLoadModule("/src/garm-discovery.jsx");
  const id="11111111-1111-4111-8111-111111111111";
  for(const [kind,label] of [["repository","Repository"],["organization","Organization"],["enterprise","Enterprise"],["unknown","Unknown"],[undefined,"Unknown"]]) {
   const pool={id:"pool",provider_name:"not-the-owner",owner_kind:kind,configured:false,enabled:true,max_runners:4,min_idle_runners:0,priority:2,tags:["linux","<script>no</script>"]};
   if(kind && kind!=="unknown")pool.owner_id=id;
   const html=renderToStaticMarkup(React.createElement(GarmPoolInventory,{inventory:{pools:[pool],missing_configured_pool_ids:[]}}));
   assert.match(html,new RegExp(`Owner: ${label}`));
   if(pool.owner_id)assert.ok(html.includes(id));else assert.ok(!html.includes(id));
   assert.match(html,/Future-runner tags: linux, &lt;script&gt;no&lt;\/script&gt;/);
   assert.match(html,/Owner metadata does not grant access/);
   assert.doesNotMatch(html,/<form|type="submit"|<script>|Apply reviewed|Review Garm change/);
  }
 }finally{await server.close();}
});
test("provider display filter has an explicit stable accessible name",async()=>{
 const server=await createServer({server:{middlewareMode:true,hmr:false},appType:"custom"});try{
 const {GarmInventoryBrowser}=await server.ssrLoadModule('/src/garm-inventory-browser.jsx');const html=renderToStaticMarkup(React.createElement(GarmInventoryBrowser,{rows:[],kind:'pools',children:()=>null}));assert.match(html,/<select aria-label="Filter pools provider"/);
 }finally{await server.close()}
});

test("pool provider summary counts the whole observed scope, not hosts or paginated rows", async () => {
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom" });
  try {
    const { GarmPoolInventory } = await server.ssrLoadModule("/src/garm-discovery.jsx");
    const pool = (id, provider_name, enabled) => ({ id, provider_name, enabled, configured: false, max_runners: 4, min_idle_runners: 1, priority: 0, tags: [] });
    const pools = [
      ...Array.from({length: 100}, (_, i) => pool(`pool-${i}`, "provider-a", true)),
      pool("beyond-page-a", "provider-a", false),
      pool("beyond-page-b", "provider-b", false),
    ];
    const render = (pools) => renderToStaticMarkup(React.createElement(GarmPoolInventory, { inventory: { pools, missing_configured_pool_ids: ["not-observed"] } }));
    const html = render(pools);
    assert.match(html, /Observed pools by configured provider/);
    assert.match(html, /Provider-to-host routing is unverified/);
    assert.match(html, /provider-a: 100 enabled · 1 disabled/);
    assert.match(html, /provider-b: 0 enabled · 1 disabled/);
    assert.doesNotMatch(html, /Pool beyond-page|not-observed: \d|<form|type="submit"/);
    const many = render(Array.from({length: 21}, (_, i) => pool(`distinct-${i}`, `provider-${String(i).padStart(2, "0")}`, true)));
    assert.match(many, /Showing the first 20 of 21 observed providers/);
    assert.doesNotMatch(many, /<li>provider-20:/);
    assert.match(many, /<option value="provider-20">/);
    const empty = render([]);
    assert.match(empty, /No pools observed in this provider scope/);
    assert.doesNotMatch(empty, /Observed pools by configured provider/);
  } finally { await server.close(); }
});
