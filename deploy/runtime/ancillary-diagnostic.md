# Closed ancillary evidence, not archive acceptance

The existing explicitly selected, no-upload format probe records nullable Engine
server/API/GitCommit/platform/driver information and bounded legacy-extra
diagnostics in the same run. Default PR behavior, archive acceptance and import
behavior are unchanged. Neither raw Docker info nor archive content is emitted.

For each existing single-role sample, Docker and direct OCI references are
excluded from the ancillary set. Every remaining regular blob must have a
canonical SHA256 filename, match that digest and fit 256 KiB before JSON parsing.
At most 256 extra nodes are considered. Output contains only fixed schema/count
fields: unknown/legacy-shaped nodes, invalid/duplicate IDs, roots/edges, missing
parents, cycles, terminals and terminal shared-configuration matches.
diagnostic_only is true and derivation_verified is always false.

A single syntactically closed chain is not proof that these blobs are legitimate.
The terminal comparison covers only shared final config fields, not Go struct
defaulting or serialization. No IDs, filenames, configuration values, environment,
commands, labels or parent values appear in output.

Official classic Moby export generates neutral intermediate V1Image nodes from
ordered DiffID prefix ChainIDs, and a final node from the image V1Image projection.
It does not copy per-layer history. Exact ID generation involves Go JSON
serialization before later saved ID/Parent/OS assignment. Engine GitCommit
observation is required to bind the applicable source/version; deriving every
node and checking actual full bytes is separate future work. This diagnostic
neither normalizes/removes extra blobs nor changes any verifier to allow them.
