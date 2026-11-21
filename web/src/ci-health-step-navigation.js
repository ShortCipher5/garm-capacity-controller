// Act only during this click. There is no stored or deferred navigation intent
// that could carry across a replacement, expiry, or different owner session.
export function focusCIStep(root, id, stillCurrent) {
  if (!root?.isConnected || typeof stillCurrent !== "function" || !stillCurrent()) return false;
  const row = root.ownerDocument.getElementById(id);
  if (!row?.matches("tr[data-ci-step-row]") || !root.contains(row)) return false;
  const steps = row.closest("details[data-ci-job-steps]");
  const attempt = steps?.closest("details[data-ci-attempt]");
  if (!attempt || !root.contains(attempt) || !stillCurrent()) return false;

  attempt.open = true;
  steps.open = true;
  row.focus();
  return true;
}
