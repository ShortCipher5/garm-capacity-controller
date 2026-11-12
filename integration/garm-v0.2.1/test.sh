#!/usr/bin/env bash
# Complete source/API/SQL/JIT/poll gate, then actual compiled bridge fixtures.
set -euo pipefail
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
work=${1:?Usage: test.sh NEW-DISPOSABLE-DIRECTORY}
[[ "$work" = /* ]] || work="$PWD/$work"
# One fresh pinned source tree; rejects existing paths and noncanonical patches.
# Includes all touched source packages, strict original JIT cleanup tests,
# existing provider workers, actual SQL preparation repeats and source vet.
"$here/preparation-test.sh" "$work"

# Qualify the actual pinned external provider against our compiled executable and
# actual gateway service, using only disposable local mTLS Garm/Incus fixtures.
cd "$here/../.."
GCC_PINNED_GARM_WORKTREE="$work" go test -race -count=1 -timeout=8m \
  -run '^TestPinnedGarm(CompiledProviderWire|ActualSourcePreparationWire)$' ./cmd/capacity-gateway
