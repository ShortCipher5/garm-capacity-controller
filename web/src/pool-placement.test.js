import assert from "node:assert/strict";
import test from "node:test";
import { poolPlacementSummary } from "./pool-placement.js";

test("placement diagnostics distinguish unconfigured, paused, and missing hosts", () => {
  const hosts = Object.freeze([Object.freeze({id:"a",new_claims_paused:false}),Object.freeze({id:"b",new_claims_paused:true})]);
  assert.equal(poolPlacementSummary({hosts:null},hosts),"No placement hosts configured.");
  assert.equal(poolPlacementSummary({hosts:[]},hosts),"No placement hosts configured.");
  assert.equal(poolPlacementSummary({hosts:["a","b","c"]},hosts),"1 host gates not paused · 1 paused · 1 unavailable in this snapshot");
  assert.equal(poolPlacementSummary({hosts:["b"]},hosts),"0 host gates not paused · 1 paused · 0 unavailable in this snapshot");
  assert.equal(poolPlacementSummary({hosts:["a","a"]},hosts),"1 host gates not paused · 0 paused · 0 unavailable in this snapshot");
  assert.equal(hosts.length,2);
});

test("ambiguous or malformed host observations never count as unpaused gates", () => {
  assert.equal(poolPlacementSummary({hosts:["a","b"]},[{id:"a",new_claims_paused:false},{id:"a",new_claims_paused:false},{id:"b"}]),"0 host gates not paused · 0 paused · 2 unavailable in this snapshot");
  assert.equal(poolPlacementSummary({hosts:["a"]},[]),"0 host gates not paused · 0 paused · 1 unavailable in this snapshot");
});
