# Inert launch-intent contract for the future M2 actor

`internal/supervisorlaunch` is a bounded, immutable DATA compiler. It has no
executor, callback/backend injection, OS/syscall/runtime imports, enrollment,
activation switch, service endpoint or application consumer. Neither the compiler
nor its tests launches a process or creates sockets/native resources. It has no
M1/checkpoint-fix or read-only resolver dependency and republishes neither held
payload. Parsed input cannot start, retry, stop or signal work or free a charge.

## Concrete interface and actual no-fallback behavior

The compiler accepts exactly eight non-null string keys:

- `protocol`: `garm-supervisor-intent/v1`
- `operation`: `single_native_birth_intent`
- `profile`: `linux-protected-init-v1` (a prototype, NOT a qualified profile)
- `scope`: 64 lowercase hex characters naming a CLAIMED scoped identity
- `attempt`: 32 lowercase hex characters naming a CLAIMED attempt
- `init_artifact_sha256`: 64 lowercase hex characters naming a CLAIMED artifact
- `workspace_ref`: 64 lowercase hex characters naming a CLAIMED protected mapping
- `payload_ref`: 64 lowercase hex characters naming CLAIMED job input

Input is at most 4096 bytes. Duplicate, case-folded, missing, unknown, wrong-type,
null, malformed, invalid UTF-8 and trailing values fail with a zero intent.
Alternate operations/profiles and executable/path/argv/env/UID/FD/mount/backend/
legacy/fallback/qualified/proof fields are refused, not forwarded or ignored.
There is no fallback parser, executor route or cleanup/retry callback.

Canonical serialization and SHA-256 identify only these syntactically checked
claims. Returned claims, canonical bytes and requirements are copies. `Parsed`
means syntax only. Even invented all-zero artifact hashes can parse; their test
explicitly demonstrates NO artifact qualification. `ActivationAuthorized` is
always false, including zero values; `ExecutionAuthority` is always `none`.
`NativeRequirements` are fixed requirements-only data, never evidence:

- clone3 into an independently qualified cgroup FD, with a birth-time pidfd;
- qualified immutable init through verified fd-bound execution;
- a fixed broker-owned host environment and qualified descriptor role mapping;
- fallback `never`, qualification `unimplemented`, execution authority `none`.

This is actual closed-ingress refusal, not a simulated native launch or proof
that a kernel/runtime has no fallback. The package has no native launch port to
invoke. Source-contract tests guard its imports/exported ports; they are not a
universal OS-isolation proof. Changing a digest/attempt/profile never grants
permission or creates/consumes an activation. No raw claims or returned digest
may be promoted to a verified binding, runtime handle or execution token.

## How this advances the concrete launcher without pretending to implement it

The upcoming actor must resolve references from its own separately authenticated,
protected enrollment and policy store. It must NEVER accept raw host paths,
integer FDs, environment, UID mappings, mount options or executable strings from
this ingress. The fixed profile must map to independently verified image/init
bytes and protected native handles. Workspace/payload references must resolve to
qualified held objects, not to a caller-chosen path or a matching hash alone.
The actor must bind the scope/attempt to authoritative immutable placement/native
incarnation and external rollback history; this compiler does none of that.

The separately reviewed sender adapter can observe packet sender credentials,
not actor code/role/job enrollment. M1 history can record claims, not provide an
independent rollback anchor. The read-only resolver is not a generic safe reader
for hostile directory trees. No combination of those observations plus this
compiler establishes eligibility or permission.

Only after the approved disposable M2 environment and native init/runtime are
qualified should an actual executor API be added in a SEPARATE review. Required
native tests remain those in `supervisor-packet-sender-observation.md`: birth
membership before intended payload, denied/closed/wrong-type/undelegated handles,
no clone3-to-fork/move fallback, fixed environment/descriptors, every runner entry
path, held pre-seal queue frontier, foreign migration exclusion and crash/restore
refusal. A failed native start can involve setup or child birth: it is not proof
of zero native attempt and cannot make an attempt reusable.

No native launch/isolation test runs in this compiler slice. Job assignment and
natural consumption, installed runner/init/runtime qualification, independent
rollback/old-writer fences, physical deletion history and accounting finalization
remain separate unimplemented prerequisites. No automatic drain or release is
enabled.

Focused check:

```sh
go test -race -count=5 ./internal/supervisorlaunch
go vet ./internal/supervisorlaunch
```
