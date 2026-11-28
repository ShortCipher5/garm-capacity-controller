import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";

test("actual observer dashboard distinguishes OS, unavailable pool, stale data and guests", async () => {
  const html=readFileSync(new URL("../../internal/observerview/dashboard.html",import.meta.url),"utf8");
  const js=readFileSync(new URL("../../internal/observerview/dashboard.js",import.meta.url),"utf8");
  const item=()=>({children:[],handlers:{},append(...v){this.children.push(...v)},replaceChildren(){this.children=[]},addEventListener(k,f){this.handlers[k]=f},click(){return this.handlers.click()}});
  const ids=Object.fromEntries([...html.matchAll(/ id="([^"]+)"/g)].map(m=>[m[1],item()]));
  const token="ab".repeat(32);
  ids.token.value=token;
  ids.auto.checked=true;
  const at="2026-01-01T00:00:00Z";let tick,requests=0;
  const doc={hidden:false,createElement:item,getElementById:id=>ids[id]};
  vm.runInNewContext(js,{document:doc,setInterval:(fn,ms)=>{assert.equal(ms,30000);tick=fn},fetch:async(path,opts)=>{
    requests++;assert.equal(path,"/admin/api/v1/observer");assert.equal(opts.headers.Authorization,"Bearer "+token);
    return {status:200,json:async()=>({mode:"observation_only",observed_at:at,hosts:[],whole_hosts:[
      {host:"host-a",status:"partial",last_observed_at:at,sample:{cpu_idle_basis_points:0,available_ram_mib:40,ram_mib:80}},
      {host:"host-b",status:"stale",last_observed_at:at,sample:{available_ram_mib:999}},
    ]})}
  }});
  await ids.refresh.click();
  const rows=ids.headroom.children.map(n=>n.textContent);
  assert.match(rows[0],/CPU idle 0 %/);assert.match(rows[0],/RAM available 40 MiB of 80 MiB/);assert.match(rows[0],/\/ available unobserved/);assert.match(rows[0],/Runner storage pool: unavailable/);
  assert.match(rows[1],/stale/);assert.doesNotMatch(rows[1],/999/);assert.match(ids.hosts.children[0].textContent,/No Incus/);
  doc.hidden=true;tick();assert.equal(requests,1);doc.hidden=false;tick();assert.equal(requests,2);
});
