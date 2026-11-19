# Explicit local pilot image export candidate

`pack-pilot-images.py` is opt-in only. No workflow or default test invokes its
Docker path. It never uploads, pushes, builds, starts, commits or imports an
image/container. This is not permission to publish artifacts or deploy them.

Input must be a reviewed joined **v2** result: v1 is deliberately refused because
it lacks ledger image and source/run identity. V2 adds `ledger_image`, exact
`source_commit`, decimal-string `run_id` and `run_attempt` to the existing v1
fields, requires `exact_four_ipv4_loopback_listeners_verified=true` and both
`gateway_executable_sha256` / `ingress_executable_sha256` as lowercase 64-hex
values, and uses schema `disposable-three-service-preboot/v2`. Its producer must
record the actual initializer image used in that same successful run. No producer
is changed here. The input evidence is not a signature: the caller must obtain
it through a trusted reviewed run, not accept arbitrary supplied JSON as proof.

The explicit expected commit must match the actual tested checkout, which may
be a synthetic PR merge commit rather than the PR head. The canonical manifest
preserves the two observed executable hashes as provenance from that joined run;
archive validation does not extract binaries or independently measure those
executables. No runtime or admission qualification is inferred. Stock image ID is pinned to the reviewed
amd64 candidate. All four role IDs must be distinct immutable sha256 IDs. The
only daemon operation is `docker --host unix:///var/run/docker.sock image save`
with those IDs. There is no inherited remote context. Export is capped at 8 GiB
and 180 seconds. The new output directory must not exist and its parent must be
canonical, owned by the current user and not group/world writable. Existing
outputs are never adopted or overwritten; failures leave private partial files
without a success manifest.

Archive verification does not extract files. It accepts only a Docker image-save
manifest, config JSON and referenced uncompressed layer files (plus directories),
with no tags, links or unrelated files. Config hashes must exactly match role IDs,
platform must be linux/amd64 and each layer hash must match its config diff_id.
Other Docker/OCI formats fail closed; compatibility with the actual hosted Docker
export still requires a future explicit exercise. Archive bytes are not claimed
reproducible; the canonical JSON manifest records their exact checksum and size.
No runtime fixture directory, container snapshot or key is included by this path.
Image provenance still matters: this is not a scanner for secrets baked into an
unreviewed image.

Integration prerequisites: use the reviewed successful v2 producer; export
while the exact tested images still exist; qualify archive-format compatibility;
choose an approved private delivery channel. Do not add automatic CI upload or
registry publication as a side effect. The mock tests produce synthetic image
tars only and do not establish an archive of the actual qualified images.

## Explicit hosted format measurement

The existing packaging workflow has a manual-dispatch boolean
`archive_format_probe`, default **false**. Ordinary PR runs cannot enable it.
When explicitly selected, a success-only phase inside the packaging harness, after the joined
fixture cleanup but before the outer EXIT trap removes its unique ingress image,
consumes that invocation's joined v2 result and checks actual checkout,
run ID and attempt before saving its exact four local images. No archive is
generated in normal workflows.

`probe-image-save-format.py` uses a newly owned private directory under
`RUNNER_TEMP`, the fixed local Docker socket and the same 180-second / 8-GiB
save bounds. A six-minute command timeout (with a ten-second kill grace) bounds the
overall measurement within the existing 30-minute job timeout. It only
inspects archive headers and invokes the existing strict identity validator.
Modern `index.json` / `oci-layout` markers can be reported as
`docker-and-oci` or `oci`, but do not establish a valid OCI descriptor graph.
The validator still refuses unsupported metadata. No permissive parser,
extraction, imports, runtime-state export or archive uploads are added.

A closed JSON record is printed only after temporary-directory deletion succeeds:
layout enum, bounded file/directory counts, size, SHA256 and validator outcome.
It is not written into the uploaded `qualification-results` directory.
An unsupported layout reports `refused` and
`archive_identity_verified:false`, not a qualification pass. Deployment
authorization is always false. Ordinary errors clean this invocation's files;
hard runner termination relies on disposal of the hosted runner, and cannot
produce a completed result. A future parser change requires reviewed measured
evidence and exact descriptor-closure/hash checks, not accepting all extra files.

### Structural diagnostics before modern format acceptance

A measured `docker-and-oci` marker alone does not identify the descriptor graph.
The format probe's v2 diagnostic result therefore includes only fixed field-set
flags, root/config coverage counts and allowlisted layer media-type counters.
It reads bounded JSON metadata, rejects duplicate JSON keys, verifies root
manifest descriptor size/hash before inspecting it, and never reads configuration
contents or emits names, annotations, URLs or arbitrary media types. These counts
are explicitly diagnostic, not graph-closure or layer qualification.

If the combined index does not reference all four role configs, the same opted-in
phase may sample each exact role ID separately, deleting each temporary archive
before the next. The initial save plus all role samples share an 8-GiB aggregate
byte budget and the existing six-minute outer timeout. Role summaries expose only
the fixed role name and closed structure/layout counts, with no additional
uploads. Existing archive acceptance is unchanged.

Why measurement is necessary: [Moby's exporter](https://github.com/moby/moby/blob/master/daemon/internal/image/tarexport/save.go)
writes Docker `LayerSources`/optional parent metadata and OCI descriptors; its
untagged index-root behavior must be observed for the actual runner version.
The [OCI layout](https://github.com/opencontainers/image-spec/blob/v1.1.1/image-layout.md)
and [descriptor](https://github.com/opencontainers/image-spec/blob/v1.1.1/descriptor.md)
specifications allow extensibility, but this pilot must use a deliberately
narrower, complete, locally verified closure. Unsupported formats remain refused
until a separately reviewed exact parser is qualified.
