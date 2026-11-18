# Immutable native policy and private identity substrate

This is a dormant read-only/compilation building block. It does not expose a
native mutation route, enable the stock-provider runtime, provision a host,
start a guest, retire a job or release capacity.

A policy snapshots exact controller, host, fresh domain, native/helper binary
hashes, pinned Incus version and per-pool image/flavor/resource mapping. The
compiler emits explicit empty profiles, fixed devices and a stopped container
body. It rejects VMs because guest memory limits alone do not establish a host
physical-memory ceiling. MAC filtering and bridge port isolation are requested
explicitly, but this is not proof of complete host/network confinement.

Identity is read over the real fresh domain's private Unix transport, bound to
birth-time pidfd/socket peer and binary hashes. An API certificate is observed
native identity, not a substitute TLS handshake or host hardware attestation.

The policy body is data, not permission. Before execution, the separate owner
path still needs canonical first-use claims, stopped-volume enforced quota
verification, immutable instance/operation binding and an independently
qualified exclusive host boundary. Root, native executable and its dependencies
are trusted. No actual Incus/systemd host execution has been qualified here.
