import assert from "node:assert/strict";
import test from "node:test";
import { auditFilterOptions, filterAuditEvents } from "./audit-events.js";
const events = Object.freeze([
  Object.freeze({sequence: 2, kind: "admitted", host: "b"}),
  Object.freeze({sequence: 1, kind: "denied", host: "a"}),
  Object.freeze({sequence: 4, kind: "admitted", host: "a"}),
  Object.freeze({sequence: 3, kind: "denied", host: ""}),
]);
test("snapshot filters combine exact kind and host without changing source order", () => {
  assert.deepEqual(filterAuditEvents(events).map(e => e.sequence), [4, 3, 2, 1]);
  assert.deepEqual(filterAuditEvents(events, "admitted", "a").map(e => e.sequence), [4]);
  assert.deepEqual(filterAuditEvents(events, "denied").map(e => e.sequence), [3, 1]);
  assert.deepEqual(filterAuditEvents(events, "", "a").map(e => e.sequence), [4, 1]);
  assert.deepEqual(filterAuditEvents(events, "missing"), []);
  assert.deepEqual(filterAuditEvents([], "admitted"), []);
  assert.deepEqual(events.map(e => e.sequence), [2, 1, 4, 3]);
});
test("filter choices are sorted unique nonempty values", () => {
  assert.deepEqual(auditFilterOptions(events, "kind"), ["admitted", "denied"]);
  assert.deepEqual(auditFilterOptions(events, "host"), ["a", "b"]);
  assert.deepEqual(auditFilterOptions([], "host"), []);
});
