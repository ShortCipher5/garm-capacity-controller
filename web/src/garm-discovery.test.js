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
