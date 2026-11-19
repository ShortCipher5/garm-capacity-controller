# Offline managed startup schema fixtures

These JSON documents are synthetic **test inputs, not runnable configuration**.
Repeated hashes and file references are placeholders, not trusted pins or keys.
Never copy these files into a deployed stack.

The three command-package `managed_fixture_test.go` tests decode these documents
into their actual production configuration types using strict JSON decoding.
They are consumed by ordinary `go test ./...`, without Docker or privileges.

- Ingress: validates the actual loopback HTTPS proxy constructor with an empty
  synthetic trust pool. No HTTP request, key load, listener or peer verification.
- Controller: records that the empty pilot is NOT startup-ready: the actual static
  validator requires configured pools and hosts. The fixture deliberately does
  not invent live infrastructure to bypass that constraint. A separate positive
  control supplies synthetic host/pool identifiers and ephemeral TLS from the
  existing test helper in `t.TempDir`, then removes one required field at a time.
  These keys are never attached to a service or published; temporary files are
  test-cleaned. Static validation opens no database or listener and sends no
  request. Missing pool/host failures are tested against that passing baseline.
- Writer: verifies immutable read-only intent and empty management scope, then
  exercises the real journal lifecycle only in a private temporary directory:
  missing journal refused, wrong/missing lineage refused, exact newly generated
  journal lineage accepted, no pending intents and private file mode. No TLS key
  or account is generated; no supervisor/topology/session startup is attempted.

A hosted qualification fixture still needs actual role-separated ephemeral TLS,
reviewed images and executable paths, valid controller inventory configuration,
real root-controlled paths, observed topology and explicit startup authorization.
These tests do not satisfy those prerequisites or establish a safe capacity
increase/physical retirement path. Runtime behavior and defaults are unchanged.
