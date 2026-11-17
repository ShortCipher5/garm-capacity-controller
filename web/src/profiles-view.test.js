import { test } from "node:test";
import assert from "node:assert/strict";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { createServer } from "vite";

test("profile estimates render fit counts per enabled host, including honest zeros", async () => {
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom" });
  try {
    const { ProfileEstimates, Profiles } = await server.ssrLoadModule("/src/profiles.jsx");
    const fits = renderToStaticMarkup(React.createElement(ProfileEstimates, { estimates: [{ host: "host-a", estimate: 3 }, { host: "host-b", estimate: 0 }] }));
    assert.match(fits, /~3 runners fit on host-a/);
    assert.match(fits, /~0 runners fit on host-b \(no remaining budget\)/);
    const empty = renderToStaticMarkup(React.createElement(ProfileEstimates, { estimates: [] }));
    assert.match(empty, /No enabled hosts — estimates cover enabled hosts only/);
    const view = renderToStaticMarkup(React.createElement(Profiles, { readProfiles: async () => ({ profiles: [] }) }));
    assert.match(view, /Runner-size profiles/);
    assert.match(view, /Gateway pool policy configuration is not exposed by this admin API/);
    assert.match(view, /Refresh profiles/);
  } finally {
    await server.close();
  }
});
