import React, {useEffect, useRef, useState} from "react";
import {ciSizingLease, ciSizingLeaseRemaining} from "./ci-health-sizing-client.js";

const stateText = {
  unreviewed: "Not reviewed",
  increase_advised: "Increase advised",
  unchanged: "No change advised",
  needs_manual_review: "Needs manual review",
  needs_risk_or_outcome_review: "Needs risk or outcome review",
  cohort_incomplete: "Cohort incomplete",
  coverage_unavailable: "Coverage unavailable",
  insufficient_history: "Insufficient history",
  stale_history: "Stale history",
};
const reasonText = {
  not_reviewed: "Cohort comparability has not been independently reviewed.",
  incomplete_cohort_coverage: "The reviewed cohort is not complete over the comparable history.",
  risk_evidence_present: "Risk evidence (throttling, OOM, disk-full or external-builder work) requires review.",
  unsuccessful_jobs_present: "Unsuccessful jobs are present in the reviewed cohort.",
  outcome_or_risk_unknown: "Outcome or risk review evidence is missing for some samples.",
  insufficient_primary_attempts: "Too few independent primary attempts.",
  insufficient_history_span: "The reviewed history does not span enough time.",
  missing_metric_coverage: "Metric coverage is incomplete; a missing sample is not low usage.",
  no_fresh_primary_completion: "No recent confirmed primary completion; the reviewed history is stale.",
  change_exceeds_reviewed_bound: "The implied change exceeds the reviewed advisory bounds.",
  within_oscillation_suppression_band: "The increase is inside the oscillation-suppression band and is withheld.",
};
const warningText = {
  sustained_cpu_pressure: "Sustained CPU pressure: at least half of the qualifying samples nearly saturated the installed CPU profile.",
  sustained_ram_pressure: "Sustained memory pressure: at least half of the qualifying samples nearly saturated the installed memory profile.",
  insufficient_spare_capacity: "Insufficient spare capacity: the advised increase would consume a large share of the remaining host headroom.",
};
const rejectionText = {
  unverifiable_identity: "unverifiable identity",
  foreign_scope: "different repository or source (cross-tenant)",
  incomparable_identity: "different matrix, profile or revision",
  profile_changed: "changed runner profile shape",
  stale: "outside the review window (stale)",
  duplicate: "duplicate execution",
};

export function ciSizingNavigationAvailable(enabled, read) {
  // Render eligibility only; the same-origin route authorizes every read.
  return enabled === true && typeof read === "function";
}

function At({value}) {
  return <time dateTime={value}>{value.slice(0, 19).replace("T", " ")} UTC</time>;
}
function displayAge(nanoseconds) {
  const n = BigInt(nanoseconds);
  const sec = n / 1_000_000_000n;
  if (sec < 60n) return `${sec} s`;
  const h = sec / 3600n;
  const d = sec / 86400n;
  if (d > 0n) return `${d} d ${(sec % 86400n) / 3600n} h`;
  if (h > 0n) return `${h} h ${(sec % 3600n) / 60n} min`;
  return `${sec / 60n} min`;
}
function Dimension({label, dimension, unit}) {
  return <div>
    <dt>{label}</dt>
    <dd>
      <strong>{stateText[dimension.state] ?? "Unknown"}</strong>
      {dimension.suggested !== null && <> — suggested <code>{dimension.suggested}</code> {unit}</>}
      {dimension.reason !== "" && <span className="muted"> {reasonText[dimension.reason] ?? dimension.reason}</span>}
    </dd>
  </div>;
}

// CISizingAdvisory renders one validated advisory snapshot. It has no apply
// action by construction: nothing here writes pool settings, caps, routing
// or allocations, and suggestions are kept separate from saved settings.
export function CISizingAdvisory({snapshot}) {
  const sizing = snapshot.sizing;
  const freshness = sizing.freshness;
  const rejectedByReason = {};
  for (const rejection of sizing.rejected) {
    rejectedByReason[rejection.reason] = (rejectedByReason[rejection.reason] ?? 0) + 1;
  }
  return (
    <div className="ci-sizing-advisory" data-ci-sizing-advisory="">
      <p><strong>Reviewed through:</strong> <At value={sizing.window.as_of} />
        {" "}({sizing.coverage} coverage; policy <code>{sizing.policy_version}</code>, model <code>{sizing.advisory_version}</code>)</p>
      <p className="muted">
        Workflow <code>{sizing.scope.workflow_id}</code>, repository <code>{sizing.scope.repository_id}</code>, source <code>{sizing.scope.source}</code>
        {sizing.identity.matrix !== "" && <>, matrix <code>{sizing.identity.matrix}</code></>}
        {" "}— profile <code>{sizing.identity.profile || "unknown"}</code>, revision <code>{sizing.identity.revision || "unknown"}</code>.
        Distinct matrix cohorts and changed runner profiles are never combined.
      </p>
      <dl className="ci-health-facts">
        <div><dt>Outcome</dt><dd><strong>{stateText[sizing.outcome] ?? "Unknown"}</strong></dd></div>
        <div><dt>Eligible samples</dt><dd>{sizing.samples.eligible} ({sizing.samples.successful} successful primary, {sizing.samples.retried} retried)</dd></div>
        <div><dt>Metric coverage</dt><dd>CPU {sizing.samples.qualifying_cpu}/{sizing.samples.successful}, memory {sizing.samples.qualifying_ram}/{sizing.samples.successful} qualifying</dd></div>
        <div><dt>Observed CPU peak</dt><dd>{sizing.observed.cpu_peak_milli === null ? <span className="muted">Unknown (no qualifying measurement)</span> : <><code>{sizing.observed.cpu_peak_milli}</code> millicores ({sizing.observed.cpu_saturated} near-saturated samples)</>}</dd></div>
        <div><dt>Observed memory peak</dt><dd>{sizing.observed.ram_peak_bytes === null ? <span className="muted">Unknown (no qualifying measurement)</span> : <><code>{sizing.observed.ram_peak_bytes}</code> bytes ({sizing.observed.ram_saturated} near-saturated samples)</>}</dd></div>
        <div><dt>Current runner profile</dt><dd>{sizing.installed.cpu_milli === "0" && sizing.installed.ram_bytes === "0" ? <span className="muted">Unknown</span> : <><code>{sizing.installed.cpu_milli}</code> millicores, <code>{sizing.installed.ram_bytes}</code> bytes</>}</dd></div>
        <div><dt>Freshness</dt><dd>{freshness.latest_primary_completion === null ? <span className="muted">No confirmed primary completion</span> : <><At value={freshness.latest_primary_completion} /> ({displayAge(freshness.age_ns)} old{freshness.fresh ? ", fresh" : ", stale"})</>}</dd></div>
      </dl>
      {sizing.uncertainty === "sampled_peak_bound" &&
        <p className="muted">Observed peaks are sampled-interval values; a sampled peak is not a complete upper bound for unmeasured load. Reductions are never advised automatically.</p>}
      <h4>Recommendation</h4>
      <dl className="ci-health-facts">
        <Dimension label="CPU" dimension={sizing.cpu} unit="millicores" />
        <Dimension label="Memory" dimension={sizing.ram} unit="bytes" />
      </dl>
      {sizing.host.known ? <>
        <h4>Host budget context</h4>
        <p className="muted">Caller-supplied display context for host <code>{sizing.host.host}</code>. It is not admission authority and does not prove spare capacity.</p>
        <dl className="ci-health-facts">
          <div><dt>CPU budget / used / headroom</dt><dd><code>{sizing.host.cpu_budget_milli}</code> / <code>{sizing.host.cpu_allocated_milli}</code> / <code>{sizing.host.cpu_headroom_milli}</code> millicores</dd></div>
          <div><dt>Memory budget / used / headroom</dt><dd><code>{sizing.host.ram_budget_bytes}</code> / <code>{sizing.host.ram_allocated_bytes}</code> / <code>{sizing.host.ram_headroom_bytes}</code> bytes</dd></div>
        </dl>
        {sizing.host.fit === "does_not_fit"
          ? <p role="alert" className="ci-sizing-fit-failure"><strong>Does not fit the reviewed host budget context.</strong> This is a hard fit failure, not a warning.</p>
          : <p className="muted">The reviewed profile and advice fit within the supplied host budget context.</p>}
      </> : <p className="muted">Host budget and headroom context unavailable; fit cannot be assessed.</p>}
      {sizing.warnings.length > 0 && <>
        <h4>Warnings</h4>
        <ul>{sizing.warnings.map((warning) => <li key={warning}>{warningText[warning] ?? warning}</li>)}</ul>
      </>}
      <h4>Disk</h4>
      {sizing.disk.state === "observed"
        ? <p>Observed disk usage peak <code>{sizing.disk.peak_bytes}</code> bytes across {sizing.disk.samples} of {sizing.disk.qualified} qualifying samples with independently attributable disk gauges.</p>
        : <p className="muted">Disk advice unavailable: no independently attributable disk data supports it.</p>}
      {(sizing.incomparable > 0 || sizing.rejected.length > 0) && <>
        <h4>Not combined</h4>
        <p className="muted">
          {sizing.incomparable > 0 && <>{sizing.incomparable} in-scope {sizing.incomparable === 1 ? "candidate was" : "candidates were"} incomparable (different matrix, profile or revision) and {sizing.incomparable === 1 ? "was" : "were"} not combined. </>}
          {sizing.rejected.length > 0 && <>Rejected: {Object.entries(rejectedByReason).map(([reason, count], index) => <span key={reason}>{index > 0 && ", "}{count} {rejectionText[reason] ?? reason}</span>)}.</>}
        </p>
      </>}
    </div>
  );
}

// Only an explicit user click during the authorized dashboard session reads
// the optional route; expiry clears the view without auto-refresh.
export function CISizingPanel({enabled, read}) {
  const [view, setView] = useState(null);
  const request = useRef(null);
  const owner = useRef(null);
  const refreshGeneration = useRef(0);
  const available = ciSizingNavigationAvailable(enabled, read);
  owner.current = available ? read : null;
  const current = view?.owner === read ? view : null;
  const snapshot = current?.snapshot;
  const lease = current?.lease;
  const state = current?.state ?? "idle";

  useEffect(() => () => {
    request.current?.abort();
    request.current = null;
  }, [available, read]);

  useEffect(() => {
    if (!snapshot) return;
    let timeout;
    const expire = () => {
      clearTimeout(timeout);
      const remaining = ciSizingLeaseRemaining(snapshot, lease);
      if (remaining < 1) {
        setView((current) => current?.lease === lease ? {owner: current.owner, state: "expired"} : current);
      } else {
        timeout = setTimeout(expire, Math.floor(remaining));
      }
    };
    expire();
    document.addEventListener("visibilitychange", expire);
    return () => {
      clearTimeout(timeout);
      document.removeEventListener("visibilitychange", expire);
    };
  }, [snapshot, lease]);

  function currentView() {
    return owner.current === read && refreshGeneration.current === current?.generation &&
      ciSizingLeaseRemaining(snapshot, lease) > 0;
  }

  async function refresh() {
    if (!available) return;
    request.current?.abort();
    const generation = ++refreshGeneration.current;
    const controller = new AbortController();
    request.current = controller;
    setView({owner: read, state: "loading"});
    try {
      const result = await read({signal: controller.signal});
      if (request.current !== controller || controller.signal.aborted) return;
      if (!result) {
        setView({owner: read, state: "unavailable"});
        return;
      }
      const wall = Date.now(), monotonic = performance.now();
      const lease = ciSizingLease(result, wall, monotonic);
      if (!lease) {
        setView({owner: read, state: "unavailable"});
        return;
      }
      setView({owner: read, state: "observed", snapshot: result, lease, generation});
    } catch {
      if (request.current === controller && !controller.signal.aborted) setView({owner: read, state: "unavailable"});
    } finally {
      if (request.current === controller) request.current = null;
    }
  }

  if (!available) return null;
  return (
    <section className="card ci-health-sizing" id="ci-health-sizing" aria-labelledby="ci-health-sizing-heading">
      <div className="section-heading">
        <h2 id="ci-health-sizing-heading">Runner sizing (advisory)</h2>
        <button type="button" disabled={state === "loading"} onClick={refresh}>{snapshot ? "Refresh sizing snapshot" : "Load sizing snapshot"}</button>
      </div>
      <p className="muted">Read-only advisory runner-size recommendations from separately reviewed, exactly attributable job history. Advisory only: this view leaves every scaling setting unchanged, has no apply action, and never routes jobs or delegates operations. Suggestions are not saved settings.</p>
      <p className="muted">Loading reads the latest reviewed advice; it does not collect job data or start any reconciliation. Without verified resource and outcome producers this stays unavailable.</p>
      <div role="status">
        {state === "loading" && "Reading advisory runner sizing…"}
        {state === "unavailable" && "Advisory runner sizing is unavailable. Verified job resource and outcome history are not published."}
        {state === "expired" && "The sizing snapshot expired. Load a new snapshot to view it."}
      </div>
      {snapshot && ciSizingLeaseRemaining(snapshot, lease) > 0 && <CISizingAdvisory key={current.generation} snapshot={snapshot} />}
    </section>
  );
}
