import test from "node:test";
import assert from "node:assert/strict";
import { writerRecoveryReport, writerRecoveryState } from "./garm-recovery.js";

test("writer recovery prioritizes unknown writes over refresh conflicts", () => {
 assert.equal(writerRecoveryState(), null);
 assert.equal(writerRecoveryState({ conflict: true }), "external-change");
 assert.equal(writerRecoveryState({ unavailable: true }), "unavailable");
 assert.equal(writerRecoveryState({ conflict: true, uncertain: true }), "unknown-write");
 assert.equal(writerRecoveryState({ snapshot: { write_blocked: true } }), "unknown-write");
});
test("diagnostic export is bounded and excludes credentials settings and raw errors", () => {
 const entry = { request_id: "request", kind: "pool", target: "pool-id", state: "unknown", created_at: "2026-11-18T00:00:00Z", started_at: "2026-11-18T00:00:00Z", token: "secret", reason: "private reason", desired: { token: "secret" } };
 const input = { unavailable: true, uncertain: true, error: "raw secret", snapshot: { write_blocked: true, password: "secret", pending: entry, history: Array(50).fill(entry), settings: { token: "secret" } } };
 const before = JSON.stringify(input);
 const report = writerRecoveryReport(input, new Date("2026-11-18T00:00:00Z"));
 assert.equal(report.recent_operations.length, 25);
 assert.equal(report.snapshot_freshness, "unavailable-or-stale");
 assert.equal(report.pending.request_id, "request");
 assert.equal(report.pending.started_at, "2026-11-18T00:00:00Z");
 assert.doesNotMatch(JSON.stringify(report), /secret|private reason|raw secret/);
 assert.equal(JSON.stringify(input), before);
});
test("report refuses malformed field types and control characters", () => {
 const report = writerRecoveryReport({ uncertain: true, snapshot: { pending: { request_id: "bad\nvalue", kind: {}, target: "x".repeat(257) } } });
 assert.equal(report.pending.request_id, ""); assert.equal(report.pending.kind, ""); assert.equal(report.pending.target, "");
 assert.equal(writerRecoveryReport({}), null);
});
