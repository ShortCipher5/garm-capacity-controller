import test from "node:test";
import assert from "node:assert/strict";
import { writerRecoveryEvidence, writerRecoveryEvidenceGuidance, writerRecoveryReport, writerRecoveryState } from "./garm-recovery.js";

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

test("classifies durable, historical and browser-only uncertainty without late promotion",()=>{
 const known={write_blocked:true,pending:{state:"unknown"}};
 const recorded={write_blocked:true,pending:{state:"pending"}};
 assert.equal(writerRecoveryEvidence({snapshot:known,uncertain:true}),"durable-unknown");
 assert.equal(writerRecoveryEvidence({snapshot:recorded}),"durable-pending");
 assert.equal(writerRecoveryEvidence({snapshot:{write_blocked:true,pending:{}}}),"durable-unclassified");
 assert.equal(writerRecoveryEvidence({snapshot:{write_blocked:true,pending:{state:"confirmed"}}}),"durable-unclassified");
 assert.equal(writerRecoveryEvidence({snapshot:known,unavailable:true}),"historical-unknown");
 assert.equal(writerRecoveryEvidence({snapshot:recorded,unavailable:true}),"historical-pending");
 assert.equal(writerRecoveryEvidence({snapshot:known,unavailable:true,uncertain:true}),"browser-uncertain");
 assert.equal(writerRecoveryEvidence({snapshot:{write_blocked:false,pending:{state:"unknown"}},uncertain:true}),"browser-uncertain");
 assert.equal(writerRecoveryEvidence({snapshot:{write_blocked:false,pending:{state:"pending"}}}),null);
 assert.match(writerRecoveryEvidenceGuidance("durable-pending"),/does not prove a request was sent/);
 assert.match(writerRecoveryEvidenceGuidance("historical-pending"),/not current evidence/);
 const report=writerRecoveryReport({snapshot:{write_blocked:true,pending:{state:"secret"}}});
 assert.equal(report.evidence,"durable-unclassified");
 assert.equal(report.pending.state,"");
 assert.equal(report.write_blocked,true);
});

test("process-local gateway hold is diagnostic only, current or historical",()=>{
 const snapshot={write_blocked:true,managed_client_write_hold:true};
 assert.equal(writerRecoveryState({snapshot}),"unknown-write");
 assert.equal(writerRecoveryEvidence({snapshot}),"managed-client-hold");
 assert.equal(writerRecoveryEvidence({snapshot,unavailable:true}),"historical-managed-client-hold");
 assert.equal(writerRecoveryEvidence({snapshot,unavailable:true,uncertain:true}),"browser-uncertain");
 assert.match(writerRecoveryEvidenceGuidance("managed-client-hold"),/Neither that read nor a restart proves the original outcome/);
 const report=writerRecoveryReport({snapshot});
 assert.equal(report.evidence,"managed-client-hold");
 assert.equal(report.write_blocked,true);
 assert.equal(report.pending,null);
});

test("legacy and malformed optional hold markers do not create new evidence",()=>{
 for(const marker of [undefined,false,"true",[true],{}]){
  assert.equal(writerRecoveryEvidence({snapshot:{write_blocked:true,managed_client_write_hold:marker}}),"durable-unclassified");
 }
 assert.equal(writerRecoveryEvidence({snapshot:{write_blocked:false,managed_client_write_hold:true}}),null);
 for(const state of ["pending","unknown"]){
  const snapshot={write_blocked:true,managed_client_write_hold:true,pending:{state}};
  assert.equal(writerRecoveryEvidence({snapshot}),`durable-${state}`);
  assert.equal(writerRecoveryEvidence({snapshot,unavailable:true}),`historical-${state}`);
 }
 assert.doesNotMatch(writerRecoveryEvidenceGuidance("durable-unclassified"),/journal|durable/);
});

test("a failed fresh read takes precedence over a retained conflict, but not an unknown write", () => {
 const input = { conflict: true, unavailable: true };
 assert.equal(writerRecoveryState(input), "unavailable");
 assert.equal(writerRecoveryReport(input).snapshot_freshness, "unavailable-or-stale");
 assert.equal(writerRecoveryReport(input).state, "unavailable");
 assert.equal(writerRecoveryState({...input, uncertain: true}), "unknown-write");
 assert.equal(writerRecoveryState({...input, snapshot: {write_blocked: true}}), "unknown-write");
});

test("historical pending reference retains only a bounded request ID and known state", async () => {
  const { historicalPendingReference } = await import("./garm-recovery.js");
  const snapshot = {write_blocked:true,pending:{request_id:"req-1",state:"unknown",kind:"private",target:"private",resolution_available:true},history:[{reason:"private"}]};
  assert.deepEqual(historicalPendingReference(snapshot), {requestId:"req-1",state:"unknown"});
  assert.equal(historicalPendingReference({...snapshot,write_blocked:false}),null);
  assert.equal(historicalPendingReference({...snapshot,pending:null}),null);
  for (const invalid of ["bad id","<hidden>","é","x".repeat(129),"bad\n",1]) {
    assert.equal(historicalPendingReference({...snapshot,pending:{request_id:invalid,state:"pending"}}),null);
  }
  assert.deepEqual(historicalPendingReference({...snapshot,pending:{request_id:"req-1",state:"anything"}}), {requestId:"req-1",state:"unclassified"});
});
