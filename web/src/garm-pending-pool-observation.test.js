import test from "node:test";
import assert from "node:assert/strict";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { createServer } from "vite";
import { observePendingPoolSettings, pendingPoolObservationGuidance } from "./garm-pending-pool-observation.js";

const target = "11111111-1111-4111-8111-111111111111";
const repo = "22222222-2222-4222-8222-222222222222";
const request = "33333333-3333-4333-8333-333333333333";
const other = "44444444-4444-4444-8444-444444444444";
const stamp = "2026-01-01T00:00:00.123456789Z";
const before = () => ({ id: target, repo_id: repo, provider_name: "fixture", fingerprint: "a".repeat(64),
  enabled: true, priority: 1, max_runners: 4, min_idle_runners: 1, generation: 7, tags: ["b", "a"] });

function snapshot(current) {
  return { settings_available: true, write_blocked: true,
    settings: { pools: [current] },
    pending: { request_id: request, target, kind: "pool", state: "unknown", started_at: stamp },
    history: [{ request_id: request, target, kind: "pool", state: "unknown", created_at: stamp,
      before: before(), desired: { ...before(), max_runners: 3 } }] };
}

test("only visible settings distinguish previous, requested and other without changing the hold", () => {
  const old = snapshot({ ...before(), tags: ["a", "b"], fingerprint: "b".repeat(64) });
  const wanted = snapshot({ ...before(), max_runners: 3, fingerprint: "c".repeat(64) });
  const different = snapshot({ ...before(), generation: 8, fingerprint: "d".repeat(64) });
  const saved = [old, wanted, different].map(value => JSON.stringify(value));
  for (const [fixture, expected] of [[old, "matches-previous-visible"],
    [wanted, "matches-requested-visible"], [different, "differs-from-both-visible"]]) {
    const observed = observePendingPoolSettings({ snapshot: fixture });
    assert.equal(observed, expected);
    assert.match(pendingPoolObservationGuidance(observed), /does not settle the write outcome or clear the block/);
    assert.equal(fixture.write_blocked, true);
    assert.equal(fixture.pending.state, "unknown");
  }
  assert.deepEqual([old, wanted, different].map(value => JSON.stringify(value)), saved);
  assert.equal(pendingPoolObservationGuidance(null), "");
});

test("unavailable and stitched draft snapshots, or absent managed history, cannot be compared", () => {
  const fixture = snapshot(before());
  assert.equal(observePendingPoolSettings({ snapshot: fixture, unavailable: true }), null);
  assert.equal(observePendingPoolSettings({ snapshot: fixture, conflict: true }), null);
  assert.equal(observePendingPoolSettings({ snapshot: { ...fixture, settings_available: false } }), null);
  assert.equal(observePendingPoolSettings({ snapshot: { ...fixture, write_blocked: false } }), null);
  assert.equal(observePendingPoolSettings({ snapshot: { ...fixture, maximum_reduction_only: true } }), null);
  assert.equal(observePendingPoolSettings({ snapshot: { ...fixture, maximum_reduction_only: true, managed_history_available: true } }), "matches-previous-visible");
});

test("missing, ambiguous or differently scoped evidence never discloses a mismatch", () => {
  for (const alter of [
    s => { delete s.pending; }, s => { s.pending.state = "confirmed"; },
    s => { s.pending.target = "outside current scope"; }, s => { s.history = []; },
    s => { s.history[0].request_id = other; }, s => { s.history.push(s.history[0]); },
    s => { s.history[0].kind = "repository"; }, s => { s.history[0].state = "pending"; },
    s => { s.history[0].created_at = "2026-01-01T00:00:00.123456788Z"; },
    s => { s.history[0].result = {}; }, s => { s.history[0].target = other; },
    s => { s.history[0].desired = { operation: "create_disabled_pool" }; },
    s => { s.history[0].desired.max_runners = 4; }, s => { s.history[0].desired.fingerprint = "b".repeat(64); },
    s => { s.history[0].desired.generation = s.history[0].before.generation + 1; },
    s => { s.history[0].before.max_runners = 0; }, s => { s.history[0].desired.tags = ["a", "a"]; },
    s => { s.history[0].desired.provider_name = "another-provider"; }, s => { delete s.history[0].before.repo_id; },
    s => { s.settings.pools[0].repo_id = other; }, s => { s.settings.pools[0].id = other; },
    s => { s.settings.pools[0].max_runners = 3; },
    s => { s.settings.pools[0].provider_name = "another-provider"; }, s => { s.settings.pools.push(before()); },
    s => { s.settings.pools = []; },
  ]) {
    const fixture = snapshot(before());
    alter(fixture);
    assert.equal(observePendingPoolSettings({ snapshot: fixture }), null, JSON.stringify(fixture));
  }
});

test("the existing recovery panel renders the comparison only with usable evidence and preserves the block", async () => {
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom" });
  try {
    const { GarmRecoveryPanel } = await server.ssrLoadModule("/src/garm-recovery.jsx");
    const fixture = snapshot({ ...before(), max_runners: 3, fingerprint: "b".repeat(64) });
    const render = props => renderToStaticMarkup(React.createElement(GarmRecoveryPanel, { snapshot: fixture, ...props }));
    const html = render();
    assert.match(html, /matches the visible pool settings requested/);
    assert.match(html, /does not settle the write outcome or clear the block/);
    assert.match(html, /Do not resend it/);
    assert.doesNotMatch(html, /<form|<input/);
    assert.doesNotMatch(render({ conflict: true }), /matches the visible pool settings/);
    assert.doesNotMatch(render({ unavailable: true }), /matches the visible pool settings/);
  } finally { await server.close(); }
});
