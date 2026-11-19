# Explicit stock ingress image target (disposable qualification)

The Dockerfile now has a `stock-ingress` target containing the existing
`capacity-stock-ingress` executable at `/usr/local/bin/capacity-stock-ingress`.
It reuses the repository's Go 1.26.10 CGO build and Debian bookworm runtime family.
The default final target remains `gateway`; nothing publishes or runs this image
as a side effect of building the normal target.

The ingress target uses UID/GID `65531:65531`, working directory `/`, and the exact
PATH expected by the managed topology checker. Both ENTRYPOINT and CMD are empty.
It exports no ports or volumes, installs no configuration or credentials, and has
no healthcheck/startup script or package-install step. A build is not activation.

For a disposable build only, select `--target stock-ingress` explicitly using the
normal Dockerfile context. No Docker daemon build/run has been qualified here.
Before any later use, record and independently review its immutable image digest
in the private deployment lock. Mutable builder/base tags are not an attestation.

The existing prepared managed-bundle command remains authoritative:

```
/usr/local/bin/capacity-stock-ingress /run/controlplane/ingress/ingress.json
```

Prepared deployment separately supplies a protected read-only ingress directory,
TLS files, exact environment/command and network namespace. This target does not
create or mount any of them. Missing configuration exits with `stock ingress
unavailable`; no server is started. Do not substitute the artifact-export stage
for this runtime or infer actual topology/ownership from the image alone.

This packaging change is not host/namespace qualification, source ownership,
capacity enforcement, permission to start the managed bundle, or a change to any
native/runtime guard. Static tests check agreement with the existing prepared
contract; offline command tests check failure without explicit configuration.

The existing disposable hosted `deploy/runtime/test.sh` harness now explicitly
builds this target using its already digest-locked Dockerfile, checks actual image
UID/PATH/workdir/empty entrypoint and command/no ports or volumes, and invokes the
binary without configuration under the existing networkless read-only,
capability-dropped non-root sandbox. It expects exit 1 within 30 seconds and mounts
no files for this check. Only after success can its final generic runtime report
include `stock_ingress_image_checks_passed: true`. Cleanup removes only this
invocation's uniquely named ingress container and tagged image; a five-second
kill-after bounds an unresponsive Docker client after its 30-second deadline. This authored check has not been run
locally against Docker; a static test and shell syntax validation are not evidence
that the hosted image check passed.
