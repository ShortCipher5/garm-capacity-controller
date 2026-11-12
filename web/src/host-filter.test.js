import assert from "node:assert/strict";
import test from "node:test";
import { filterHosts } from "./host-filter.js";
const hosts = Object.freeze([
  Object.freeze({ id: "paused-fresh", new_claims_paused: true, observation_fresh: true, last_observed_healthy: true }),
  Object.freeze({ id: "allowed-stale", new_claims_paused: false, observation_fresh: false, last_observed_healthy: true }),
  Object.freeze({ id: "paused-unhealthy", new_claims_paused: true, observation_fresh: true, last_observed_healthy: false }),
]);
test("host attention filters use independent recorded flags, preserving source/order", () => {
  assert.deepEqual(filterHosts(hosts, "paused").map(h => h.id), ["paused-fresh", "paused-unhealthy"]);
  assert.deepEqual(filterHosts(hosts, "stale").map(h => h.id), ["allowed-stale"]);
  assert.deepEqual(filterHosts(hosts, "unhealthy").map(h => h.id), ["paused-unhealthy"]);
  assert.deepEqual(filterHosts(hosts, "all"), hosts);
  assert.notEqual(filterHosts(hosts, "all"), hosts);
  assert.deepEqual(filterHosts([], "stale"), []);
  assert.equal(hosts.length, 3);
});
