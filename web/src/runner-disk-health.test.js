import test from "node:test";
import assert from "node:assert/strict";
import { runnerDiskHealthRows } from "./runner-disk-health.js";

const row = {storage_pool:"test-pool", state:"current", observed_at:"2026-11-20T00:00:00Z", expires_at:"2026-11-20T00:01:00Z", total_bytes:"9223372036854775807", available_bytes:"0", total_inodes:"20", available_inodes:"0"};
const now = Date.parse("2026-11-20T00:00:25Z");
test("current runner storage preserves exact integer counts and real zero",()=>{
 const [sample]=runnerDiskHealthRows([row],now);
 assert.equal(sample.pool,"test-pool");
 assert.equal(sample.state,"current");
 assert.equal(sample.totalBytes,9223372036854775807n);
 assert.equal(sample.availableBytes,0n);
 assert.equal(sample.availableInodes,0n);
});
test("expired current or explicitly stale rows withdraw numerical evidence",()=>{
 const end=Date.parse(row.expires_at);
 const [sample]=runnerDiskHealthRows([row],end);
 assert.equal(sample.state,"stale");
 assert.equal(sample.availableBytes,undefined);
 const stale={storage_pool:row.storage_pool,state:"stale",observed_at:row.observed_at,expires_at:row.expires_at};
 assert.equal(runnerDiskHealthRows([stale],now)[0].state,"stale");
 assert.deepEqual(runnerDiskHealthRows([{...row,state:"stale"}],now),[]);
});
test("invalid, mixed, or unconfigured snapshots do not show numeric health",()=>{
 for (const bad of [undefined, [], {}, Array(9).fill(row), [row,row], ...[
  {storage_pool:"../root"},{storage_pool:["test-pool"]},{state:"verified"},
  {total_bytes:"9223372036854775808"},{total_bytes:15},{total_bytes:"0"},
  {available_bytes:"9223372036854775808"},{available_bytes:"-1"},{available_bytes:"00"},
  {available_inodes:undefined},{available_inodes:"21"},{observed_at:"2026-11-20T00:00:26Z"},
  {expires_at:"2026-11-20T00:00:00Z"},{expires_at:"2026-11-20T00:06:00Z"},
 ].map(change=>[{...row,...change}])]) assert.deepEqual(runnerDiskHealthRows(bad,now),[]);
});

test("impossible calendar dates, rollover times and year zero are not observations",()=>{
 for (const observed_at of [
  "2026-02-29T23:59:00Z", "2026-02-30T23:59:00Z", "2026-03-01T24:00:00Z", "0000-03-02T00:00:00Z",
 ]) {
  // The matching expiry keeps each rolled-over value inside the allowed
  // interval in implementations where Date.parse alone accepts the date.
  const rolls=Date.parse(observed_at);
  assert.ok(Number.isFinite(rolls));
  const expires_at=new Date(rolls+60000).toISOString();
  assert.deepEqual(runnerDiskHealthRows([{...row,observed_at,expires_at}],rolls+30000),[]);
 }
 const observed_at="2028-02-29T23:59:00.000000001Z";
 const expires_at="2028-03-01T00:00:00.000000001Z";
 assert.equal(runnerDiskHealthRows([{...row,observed_at,expires_at}],Date.parse(observed_at)+1000)[0].state,"current");
 const expiresRollover={...row,observed_at:"2026-02-28T23:59:00Z",expires_at:"2026-02-28T24:00:00Z"};
 assert.deepEqual(runnerDiskHealthRows([expiresRollover],Date.parse(expiresRollover.observed_at)+1000),[]);
});
