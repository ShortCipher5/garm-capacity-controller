package deployment

import (
	"strings"
	"testing"
)

// Static packaging contracts only: these do not start Docker, listen, or qualify
// a managed namespace. The executable must still receive protected runtime files.
func TestStockIngressImageIsExplicitAndMatchesPreparedRuntime(t *testing.T) {
	dockerfile := repositoryFile(t, "Dockerfile")
	start := strings.Index(dockerfile, "FROM runtime-base AS stock-ingress\n")
	if start < 0 {
		t.Fatal("missing explicit ingress runtime")
	}
	rest := dockerfile[start:]
	end := strings.Index(rest[1:], "\nFROM ")
	if end < 0 {
		t.Fatal("ingress must not replace default gateway target")
	}
	stage := strings.TrimSpace(rest[:end+1])
	expected := `FROM runtime-base AS stock-ingress
USER 65531:65531
WORKDIR /
ENV PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
COPY --from=stock-ingress-build --chmod=0555 /out/capacity-stock-ingress /usr/local/bin/capacity-stock-ingress
ENTRYPOINT []
CMD []`
	if stage != expected {
		t.Fatal("ingress runtime contract changed; review user/environment/files and startup")
	}
	if !strings.Contains(dockerfile, "FROM go-build AS stock-ingress-build\nRUN CGO_ENABLED=1 go build -trimpath -buildvcs=false -o /out/capacity-stock-ingress ./cmd/capacity-stock-ingress") {
		t.Fatal("ingress must reuse pinned Go/CGO build")
	}
	if !strings.Contains(dockerfile, "FROM debian:bookworm-slim AS runtime-base") {
		t.Fatal("review libc compatibility before changing runtime")
	}
	if strings.LastIndex(dockerfile, "FROM ") != strings.LastIndex(dockerfile, "FROM runtime-base AS gateway") {
		t.Fatal("default image changed")
	}
	prepare := repositoryFile(t, "internal/managedgarm/prepare_linux.go")
	for _, contract := range []string{`base(p.IngressImage, "65531:65531", "ingress")`, `[]string{"/usr/local/bin/capacity-stock-ingress", "/run/controlplane/ingress/ingress.json"}`, `"entrypoint": []string{}`, `"working_dir": "/"`, `"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"`} {
		if !strings.Contains(prepare, contract) {
			t.Fatalf("prepared command contract changed: %s", contract)
		}
	}
}

func TestStockIngressHostedCheckIsRestrictedAndReportedAfterSuccess(t *testing.T) {
	script := repositoryFile(t, "deploy/runtime/test.sh")
	for _, required := range []string{
		`ingress_tag="capacity-stock-ingress:${project}-${work##*.}"`,
		`docker build -f "$work/locked.Dockerfile" --target stock-ingress -t "$ingress_tag" .`,
		`docker image inspect "$ingress_tag" | python3 -c`,
		`assert c["User"]=="65531:65531" and c["WorkingDir"]=="/"`,
		`assert c.get("Entrypoint") in (None,[]) and c.get("Cmd") in (None,[])`,
		`assert c.get("Env")==["PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"]`,
		`assert not c.get("Volumes") and not c.get("ExposedPorts")`,
		`expect_exit 1 timeout --kill-after=5s 30s docker run "${restricted[@]}" --name "$ingress_container" --user 65531:65531 "$ingress_tag" /usr/local/bin/capacity-stock-ingress`,
		`docker rm -f "$ingress_container" >/dev/null 2>&1 || true`,
		`docker image rm "$ingress_tag" >/dev/null 2>&1 || true`,
		`'stock_ingress_image_checks_passed':True`,
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("missing explicit hosted ingress check: %s", required)
		}
	}
	checks := strings.Index(script, `expect_exit 1 timeout --kill-after=5s 30s docker run`)
	report := strings.Index(script, `'stock_ingress_image_checks_passed':True`)
	if checks < 0 || report < checks || !strings.Contains(script, "set -euo pipefail") {
		t.Fatal("success result must follow strict successful checks")
	}
}
