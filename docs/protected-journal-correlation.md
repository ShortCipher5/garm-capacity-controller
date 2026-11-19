# Root journal and terminal job correlation (read-only)

The private native adapter can read only the fixed guest root journal
`/var/lib/garm-capacity/<exact-native-UUID>/state.json`. It checks every parent
as a root-owned non-writable real directory, exact root0600 file metadata,
canonical bytes, domain/incarnation/init digest, and brackets the read with the
same running native UUID and pinned instance configuration. It accepts only
`launch_unknown` or `process_tree_exited_unqualified`. A field claiming job
completion authority is rejected. No write/exec/stop/delete API is used.

The existing authenticated owner exposes this observation at one GET endpoint
for its in-memory canonical execution. It requires successful START and verified
bootstrap. Owner loss does not adopt an old execution. The controller performs
actual pinned owner TLS reads, then joins the result to schema23's exact native
runner binding and terminal GitHub job history in one read-only transaction.

The correlation explicitly reports acceptance fencing unqualified, physical
deletion unproven and release unauthorized. Even root journal exit + GitHub
completed is NOT a retirement permit. Dedicated image/PID1/namespace behavior,
credential containment and physical lifecycle ownership still need qualification.
The owner command's activation refusal remains unchanged.

Local tests exercise actual HTTP metadata/byte reads against disposable fixtures,
pinned controller/owner evidence reads, wrong ownership/incarnation/authority
rejection and zero release permission. They do not claim live Incus execution,
privileged guest boot or complete containment. No live hosts were contacted.
