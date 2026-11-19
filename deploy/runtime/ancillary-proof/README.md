# Exact Moby v28 ancillary byte proof, not archive acceptance

This stdlib-only helper is built only by the existing explicitly opted-in
no-upload format probe. No service, default build target or live host uses it.
The private adapter reads bounded original config/auxiliary bytes directly from
the temporary per-role archive, without extraction, and sends them over stdin.
No original bytes, arbitrary paths, configuration values or raw errors are logged.

The helper requires observed server28.0.4/API1.48/GitCommit6430e49 (or its full
commit)/linux-amd64/overlay2 and null image-store metadata. Null is not a claim of
classic-store identity: exact byte derivation provides separate corroboration.
The expected config SHA256 binds the immutable role image. Every auxiliary digest
is checked before reproducing the complete expected node set using pinned Moby
v28.0.4 V1Image/Config defaults, ordered DiffID prefix ChainIDs and Go JSON.
The generated neutral epoch timezone is taken from consistent observed zero-
instant timestamps and constrained to whole-minute offsets within UTC±14.
It is confirmed by byte-for-byte comparison, not used to change any host setting.
Optional Healthcheck data is unsupported and refuses proof.

Only matched/derived counts and ancillary_derivation_verified can become true.
archive_accepted and deployment_authorized remain false, even when every
ancillary node matches. The legacy archive validator and separate strict-closure
building block are not changed or bypassed. An unavailable proof reports a closed
failure status rather than turning other observed diagnostics into a success.

Bounds: config16MiB, at most256 auxiliary nodes each256KiB, private JSON input112MiB,
helper30seconds, optional build180seconds, whole existing probe360seconds and job
30minutes. Temporary helper/archive paths are owned by the existing cleanup.
No executable/image archive upload or import is introduced. Go/Python fixture
tests do not replace an exact hosted proof of the measured four images.
See NOTICE and LICENSE for pinned upstream source and modified declarations.

The optional build uses a fresh named context containing exactly the six reviewed
Go files plus LICENSE and NOTICE. The ordinary checkout context allowlist remains
unchanged; no deploy subtree, sibling fixture, token, or extra Go file is copied.
