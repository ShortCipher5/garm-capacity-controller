# Licensing scope and third-party notices

## Project material

The project's original source, documentation and configuration in the tree
containing this notice are provided under the Apache License, Version 2.0 in
[LICENSE](LICENSE), except material identified with other terms. This applies to
existing project material in this tree, not only files added after this notice.
It does not rewrite earlier commits or represent that every historical release
already contained a license. It does not relicense third-party material, remove
existing notices, or claim another contributor's copyright. Existing specific
file notices and third-party terms remain applicable.

## Garm and garm-provider-common

- Garm source: <https://github.com/cloudbase/garm/tree/154638445c3949c1958b01812f69d9a1e4d82684>.
- Garm license: [pinned upstream LICENSE](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/LICENSE), Apache 2.0, reproduced in this repository's LICENSE.
- Garm file copyrights relevant to modified existing files include `Copyright
  2022 Cloudbase Solutions SRL`, `Copyright 2024 Cloudbase Solutions SRL` and
  `Copyright 2025 Cloudbase Solutions SRL`. They remain with their owners.
- garm-provider-common is an Apache 2.0 dependency. Its upstream license carries
  `Copyright 2023 Cloudbase Solutions SRL`; retain that notice when distributing
  its code. See <https://github.com/cloudbase/garm-provider-common> and the
  exact dependency version in `go.mod`/`go.sum`.

`integration/garm-v0.2.1/admission-preflight.patch` contains upstream context plus
project modifications, including configuration, SQL validation, runner/provider
preparation and cancellation, and workers. Garm-related `testdata` templates,
`integration/garm-v0.2.1/` harnesses and `deploy/runtime/garm-*` probes are local
integration/qualification material, not an upstream Garm release. Existing
upstream copyright/license headers are not replaced by this project's license.
The inspected Garm commit has no top-level NOTICE file; its vendored dependencies
have their own licenses and, in some cases, NOTICE files.

The patch is not changed by this licensing-only update. When distributing
assembled modified Garm source, retain its original headers and put a prominent
change notice in each modified upstream file as required by Apache 2.0 section
4(b). Include the applicable license and upstream/dependency notices with source
and binary distributions. The presence of this repository-level notice alone is
not a claim that an assembled image meets every redistribution obligation.

## Incus

The dormant native qualification patch at
`deploy/runtime/incus-native/reviewed-native.patch` and the related
`deploy/runtime/native-{caller,retry,wireloss}/` qualification files target
[Incus 6.0.4 at a99413afd0c742da3bba10eb4f75d639f9dbbd32](https://github.com/lxc/incus/tree/a99413afd0c742da3bba10eb4f75d639f9dbbd32).
Upstream [COPYING](https://github.com/lxc/incus/blob/a99413afd0c742da3bba10eb4f75d639f9dbbd32/COPYING)
contains Apache 2.0. Its [AUTHORS](https://github.com/lxc/incus/blob/a99413afd0c742da3bba10eb4f75d639f9dbbd32/AUTHORS)
identifies file headers and Git history as authorship records and preserves
specific file exceptions. Do not replace those records or the licenses/notices
of its dependencies with a blanket project copyright claim.

## Mozilla/curl public CA bundle: MPL 2.0 exception

`deploy/runtime/release-profile/public-ca.pem` is a converted Mozilla root
certificate bundle distributed by curl's [CA Extract](https://curl.se/docs/caextract.html)
service. That service specifies MPL 2.0 for the converted bundle, the same
license as Mozilla's source. This file is **not** relicensed under Apache 2.0.

- Its existing source/conversion/date metadata is retained unchanged.
- The full MPL 2.0 text is in [LICENSES/MPL-2.0.txt](LICENSES/MPL-2.0.txt).
- The covered source form is the committed PEM file itself; Mozilla's upstream
  source reference is recorded in its header. Keep this source form and the
  MPL notice available when redistributing a bundle or image containing it.
- This notice adds licensing information only. It does not change the trust
  store, certificates, fingerprints or application behavior.

## Downloaded and linked dependencies

Go modules and npm packages remain under their own licenses. `go.mod`, `go.sum`
and `web/package-lock.json` record dependency selections, not a relicensing or
an exhaustive license inventory. For example, linked Go dependencies and bundled
React/React DOM assets retain their applicable upstream terms; building Garm or
Incus also introduces their own vendored dependencies and notices. The Go/Node
runtimes, operating-system packages, container base images and downloaded tools
are separately licensed too.

Before distributing a binary, UI bundle or container image, inventory its exact
contents and ship all required upstream license/copyright/NOTICE files and any
required corresponding source. This source-only PR is not a completed artifact
license audit and does not authorize image publication or deployment.
