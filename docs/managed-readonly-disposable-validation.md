# Disposable managed read-only startup validation design

Status: **design and offline configuration contract only; no stack qualified**.
This is not a deployment recipe or permission to launch containers. Keep stock
GARM unmodified. Neither a native ZFS runtime nor a runner provider is needed for
an empty read-only control-plane fixture.

## What is checked without starting anything

`TestGeneratedComposeMatchesPureTopologyContract` serializes the actual
`composeDocument`, decodes its role configuration and projects only declared
fields into the existing pure topology validator. It checks the three roles,
profile opt-in, no restart, bounded PID/tmpfs configuration, namespace sharing,
public port, mount access, image IDs, commands, environment and role identities.
Synthetic process IDs/running flags are explicitly placeholders. No `Observe`,
`Prepare`, Docker call, credential creation or namespace operation occurs.

This closes a gap between independently handwritten topology fixtures and the
actual generated plan. Matching configuration does **not** prove executable
existence, inherited image settings, actual mount permissions, TLS, bootstrap,
network listeners or startup. In particular, a shared wrong executable path in
both generator and validator still passes; the separate pinned-image lab must
supply real executable evidence.

Existing `managedwriter/readonly_linux_test.go` covers authenticated GETs,
write-disabled advertisement, mutation rejection before body/journal/upstream
access and disposable TLS. Existing bootstrap tests cover first-run uncertainty
and restart markers. None joins these pieces into a real managed startup.

## Inputs required before a future hosted fixture can start

- Reviewed immutable local images for each role, containing the exact executable
  expected by the generated command. Check inherited environment/entrypoint and
  ABI as well as path; a lab discovery result alone does not alter the contract.
- Fresh root-controlled private directory and preparation input. The generated
  Compose profile stays opt-in; no existing data or installation may be adopted.
- Controller configuration/state initialization, ingress configuration and TLS.
  `Prepare` does not supply `controller-config/gateway.json`,
  `ingress/ingress.json` or the ingress server key.
- Root supervisor specification populated with actual container/image IDs and
  stock TLS pin, plus protected proxy configuration, independent mTLS roles,
  journal and lineage. The proxy config must explicitly set `read_only: true`.
- A fresh hosted Docker runner capable of the required root peer inspection and
  network namespace operations. Our ordinary unprivileged Go tests cannot
  establish these capabilities.

## Proposed future hosted-only sequence (not implemented here)

1. Create only fresh fixture-owned inputs; start the generated three services.
   Inspect actual image defaults, UID/capabilities, mounts, ports and namespace
   membership with the real observer, not a fixture-created observation.
2. Bootstrap empty stock GARM once, then reopen authentication. No repository,
   provider, GitHub credential, runner or job is configured. Check first-run is
   not repeated after reopening.
3. Start the real supervisor proxy in immutable read-only mode. Verify scoped
   settings/inventory/demand GETs and `writes_enabled:false`. Verify mutation
   requests fail before upstream writes and leave no pending journal intent.
4. Restart the proxy, confirm read-only remains active; deliberately drift one
   fixture topology fact and confirm access fails closed. Do not weaken guards
   to make a hosted runner pass.
5. Remove only resources owned by this fixture. Emit public status, never raw
   configs, Docker inspection dumps, tokens, keys or private bootstrap records.

The existing hosted packaging workflow is a potential home, not proof this
sequence runs today. A future pass establishes only disposable empty managed
read-only startup. It cannot establish real-provider runner creation, global
capacity enforcement, safe physical retirement or installed-host readiness.
