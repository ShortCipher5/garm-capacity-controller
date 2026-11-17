import { test } from "node:test";
import assert from "node:assert/strict";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { createServer } from "vite";

const load = async () => {
  const server = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom" });
  try {
    return { server, module: await server.ssrLoadModule("/src/usage-chart.jsx") };
  } catch (error) {
    await server.close();
    throw error;
  }
};
const sample = (at, cpu) => ({ host: "host-a", sampled_at: at, cpu_used_millis: cpu, ram_used_mib: 1024, disk_used_mib: 2048, source: "fixture-collector" });

test("usage chart draws one polyline per contiguous segment and never invents points", async () => {
  const { server, module } = await load();
  try {
    const { UsageChart, formatSampleAge } = module;
    assert.equal(renderToStaticMarkup(React.createElement(UsageChart, { samples: [] })), "");
    assert.equal(renderToStaticMarkup(React.createElement(UsageChart, { samples: null })), "");
    const gapped = [
      sample("2026-01-01T00:00:00Z", 100),
      sample("2026-01-01T00:05:00Z", 200),
      sample("2026-01-01T00:20:00Z", 300),
      sample("2026-01-01T00:25:00Z", 400),
    ];
    const html = renderToStaticMarkup(React.createElement(UsageChart, { samples: gapped, dimension: "cpu_used_millis", label: "CPU used", unit: "millicores" }));
    assert.match(html, /CPU used history chart/);
    assert.equal((html.match(/<polyline/g) || []).length, 2, "gap must break the line into two segments");
    assert.equal((html.match(/<circle/g) || []).length, 4);
    assert.match(html, /· millicores/);
    const contiguous = [0, 5, 10, 15].map((minute) => sample(`2026-01-01T00:${String(minute).padStart(2, "0")}:00Z`, 100));
    assert.equal((renderToStaticMarkup(React.createElement(UsageChart, { samples: contiguous })).match(/<polyline/g) || []).length, 1);
    assert.match(formatSampleAge(new Date("2026-01-01T00:00:45Z"), "2026-01-01T00:00:00Z"), /seconds ago/);
    assert.match(formatSampleAge(new Date("2026-01-01T02:00:00Z"), "2026-01-01T00:00:00Z"), /hours ago/);
    assert.match(formatSampleAge(new Date("2026-01-03T00:00:00Z"), "2026-01-01T00:00:00Z"), /days ago/);
  } finally {
    await server.close();
  }
});
