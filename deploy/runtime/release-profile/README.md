# Disposable historical Garm image/profile qualification

Status at draft preparation: native synthetic/static checks passed; Docker image
execution had not run. The standalone hosted workflow tests only NEW generated
fixtures. No result qualifies an installation, production start, restore or deploy.

## Exact inputs and graph

prepare.py accepts only immutable PUBLIC application object archives:
- Garm154638445c3949c1958b01812f69d9a1e4d82684;
- controller e44d7a9ff11f70e52eded5b1d706db7cd0c8e707,
  treea322259d30a04927bccb63e2ac9c40e3274c1ac8;
- canonical patch from THAT public controller archive, full SHA256
  87e0d61751d4b9ffa15322cff09557f0a1094fdd68464fe840a12ffe01bb0660.

No application branch or current working tree is compiled. These are historical
source contracts, not current-controller qualification. Git replacement objects,
global credentials/config and interactive auth are disabled for archive/fetch.
Tar extraction rejects links/devices/traversal and bounds source size/count. Only
explicit NEW fixture-code files and the pinned PUBLIC CA content enter context;
no existing key/config/state/HOME/cache/output directory is copied. Public upstream
source may contain its own public test fixtures; no generated/installed credential
is supplied. Testsrc/probe is qualification-only code, exported separately and never included
in the daemon or maintenance image.

lock.json is the retained candidate with canonical SHA256
e9e29855fdfea8369c0e1fa76d5ff376666ced43aff04b15ee86c63150193c88.
Its official registry index/child manifest JSON relationship was separately checked,
not image/layer/config bytes or signatures. Rendered recipe pins all external FROMs
and the frontend to linux/amd64 child digests. Tag re-resolution is forbidden.

Dockerfile.in has six explicit stages:
1. locked Node22/npm10.9.9, lockfile install with scripts disabled, closed production
   environment, upstream Svelte build, full public98-entry embedded inventory;
2. locked Go1.26.10, Garm/CLI vendor CGO1, controller public modules download/verify
   and actual CGO1 provider, separate static probe + synthetic anchor;
3. scratch public artifact export (application binaries, probe/anchor, inventory);
4. locked Debian daemon image: Garm/provider/public CA, USER65531, version default;
5. separate locked Debian tools image: CLI/OWN public CA, USER65533, help default;
6. explicitly BROKEN scratch Garm image without loader, negative-only.

The same recipe's sole permitted build argument is PROFILE_VARIANT=baseline or
missing-js. The latter records the original full inventory, removes one generated
JS BEFORE embedding, and has its own binary/image identity. No secret build arg,
external COPY source, RUN mount/host network, ONBUILD, escape/heredoc/continued FROM
or frontend override is allowed. The restrictive parser is NOT a general Docker
security validator; the actual full graph still needs independent review.

Public CA content is exactly SHA256
427c064216761a1e72a8b30f0d954d08cb6e4435a82091fb865afd20c8497285 from
https://curl.se/ca/cacert.pem (mutable origin, retained content digest). Its mutable origin is NOT an independently
approved production trust pin; no host trust store is read/copied or changed.

## Executable runner boundaries

run.py takes NO input path/host/credential argument and refuses non-GitHub-hosted
context BEFORE any command. Those environment markers are CONVENTIONS, not an
authorization or adversarial host detector. Never run this on a user/installed host.
It is intended only for a future explicitly approved hosted job. Docker is forced
to local unix:///var/run/docker.sock with a NEW empty CLI config/HOME. Remote Docker/
BuildKit context overrides refuse. Docker subprocess environment excludes workflow
credentials and ambient Docker config; no login/push/private registry is used.
The hosted daemon/backend remains trusted, not hermetic or signed.

All baseline/negative builds and public artifact exports finish before THAT lab's
runtime keys/config/state are created. Earlier native unit fixtures are isolated,
deleted and never part of the fresh context. Runtime creates ONLY NEW generated
role directories in the new runner temp root. A bounded sudo chown applies ONLY to
these generated leaves for65530/65531/65533. No existing source/state is repaired,
initialized, reowned, adopted or restored. Current native tests use change_owner=False
and assert only OpenSSL commands ran; no sudo/owner change has run locally.

Every created container is unique/labeled, scoped by its known image ID. All caps
are dropped, NNP, readonly root, private PID, fixed memory/CPU/PID bounds, narrow
private tmpfs/mount allowlists and no published port/host network/socket. Actual
image defaults are inspected BEFORE test --user overrides, so override-based UID
checks cannot hide a wrong default. One-shot containers are removed immediately;
readiness retries ONLY typed transport unavailability, not CA/name/config errors.
Generic nonzero, engine/start failure or timeout cannot satisfy a security negative.

Planned executable cases (all CURRENTLY UNRUN):
- Both actual image public CA file hashes, all final application/exported hashes,
  actual Go build info/version+CGO and ELF loader/library structure;
- one NEW loopback-only TLS anchor with disjoint private key from Garm; fresh probe
  process per actual default-root-file trust case, empty/unrelated CA→typed42,
  wrong name on SAME route→43, repeated positives, no SSL/proxy/root override;
- owner-positive read+write and other non-root UID EACCES53 on the SAME generated
  canary path, with positives before/after (missing path/readonly FS is not denial);
- actual Garm PID1 UID/GID/groups/caps/NNP, tools self-probe boundary (NOT direct
  sampling of short-lived CLI PID1), effective loopback and expected netns;
- real new Garm TLS/UI root+SPA/all inventory bytes/MIME and missing-JS image→45,
  not only helper HTTP recorders or an HTTP200 HTML fallback;
- actual keyless scratch missing-loader structure→44, then an independently
  attributed OCI exec-missing-interpreter error (not generic start failure);
- provider config-free discovery ONLY, CLI help/version with NEW private empty
  tmpfs HOME and no profile file, Garm version label;
- baseline generated main/blob files continue with same inode/owner/private mode
  across stopped recreation; no SQLite opener/backup/restore or history proof;
- recreate namespace OWNER and all clients; require new current namespace and
  repeated image/Garm positive checks. No stale orphan namespace may pass.

Garm starts only on NEW no-account/pool/job/provider-authority state. Its explicit
TEST invocation sets umask077 then execs Garm, so PID1 is Garm. This is not a shipped
production launcher or existing-only guard; normal Garm still auto-creates/migrates
supplied state. No global old-writer fence, real issuer/job/drain or recovery exists
in this runner. Historical/latest source and production enablement are not inferred.

## Diagnostics, cleanup and artifact boundary

Only fixed phase/rejection classes and completed whitelist JSON are printed. Raw
build/runtime logs, inspect/env/config, private keys/cert hashes/nonces/controller
IDs/DBs/paths are never artifacts. Proc/netns/inode observations stay in memory and
become booleans only. Public immutable source/base/image/binary/recipe identities
can be reported; no image is pushed. Result directory creation is exclusive before
work starts, result file is exclusive AFTER successful tests AND owned cleanup.
Future artifact upload is success-only, one exact JSON file, error if absent.

Cleanup verifies owned run label/image ID and removes only its successfully created
containers/tags. Generated root cleanup uses its saved NEW temp path, never a
caller-provided source/destination. Ambiguous ownership does not cause deletion of
someone else's resource. A cleanup failure prevents a success result. No service
restart, daemon setting, network/security permission or installed-state action.

## Current native checks and future approval

Python synthetic orchestration/grammar/topology/credential-environment tests pass;
actual clean public archive context preparation was executed successfully. Native
Go probe unit tests/race/vet pass; synthetic keys/loopback TLS only. New canary and
namespace features are test code requiring new review. Native validation alone establishes no engine/default-CA/Garm UI/UID/ABI image
outcome. Those results belong to the exact hosted run. The success JSON is generated
after full tests/cleanup and is never source-controlled.

The standalone .github/workflows/disposable-release-profile.yml confines execution
to Ubuntu24 GitHub-hosted runners with contents-read permission, full-history
credential-free checkout, pinned actions and one success-only JSON artifact. It
uses no user secret, image registry push or installed host. Source review and
native tests do not substitute for its exact hosted runtime result. Neither a
draft/workflow nor a green disposable result authorizes production image registry
publication, host assessment/setup, startup, rollback, recovery or deployment.
