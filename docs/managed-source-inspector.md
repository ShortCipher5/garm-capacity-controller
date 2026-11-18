# Authenticated managed source inspection

The private writer optionally accepts a dedicated `source_inspector_leaf` and
`source_inspector_certificate_file`. This role can only GET
`/v1/managed/source`; it cannot read demand/credentials or pause pools. The
writer command reads the root-protected public certificate and rejects key reuse
with its writer-client, writer-server or stock-server identities. TLS client-chain
validation remains mandatory.

A fresh nonce-bound response follows a successful live `Session.Verify` and
contains the actual bootstrap-bound source and stock controller identities. It
uses the same provenance headers as managed demand. `managedsource.ObserveSource`
returns an opaque, freshness-limited observation only after pinned TLS,
nonce/body/header and controller checks. No configured source hash mints one.

`NewWithDialer` lets the root observer dial through its independently observed
namespace FD. It accepts only a TCP dial function, not a transport replacement;
normal TLS/name/key-pin verification remains inside managedsource. The observer
must reobserve topology around the read. A dial callback itself is not evidence.

Matching this origin to saved profiles closes source-association ambiguity. It
still does not establish GARM's loaded configuration, exclusive native mutation,
capacity-increase permission, safe retirement, or production activation.
