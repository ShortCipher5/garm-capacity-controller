//go:build cihealth_playwright_fixture

package cihealthreport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestPinnedPlaywrightJUnitReporter qualifies format compatibility only. It
// does not discover reports, authenticate a workflow, or create a producer
// receipt. The synthetic binding and reviewed ZIP are test inputs only.
func TestPinnedPlaywrightJUnitReporter(t *testing.T) {
	web, err := filepath.Abs("../../web")
	if err != nil {
		t.Fatal("cannot locate web fixture dependencies")
	}
	lock, err := os.ReadFile(filepath.Join(web, "package-lock.json"))
	var pins struct {
		Packages map[string]struct{ Version string } `json:"packages"`
	}
	if err != nil || json.Unmarshal(lock, &pins) != nil {
		t.Fatal("cannot read the pinned Playwright version labels")
	}
	for _, pkg := range []string{"@playwright/test", "playwright", "playwright-core"} {
		path := "node_modules/" + pkg
		data, err := os.ReadFile(filepath.Join(web, path, "package.json"))
		var installed struct{ Version string }
		if err != nil || json.Unmarshal(data, &installed) != nil || installed.Version == "" || installed.Version != pins.Packages[path].Version {
			t.Fatal("installed Playwright version labels must match the lockfile; dependencies must be independently trusted")
		}
	}
	node := os.Getenv("CI_HEALTH_FIXTURE_NODE")
	if !filepath.IsAbs(node) || filepath.Clean(node) != node {
		t.Fatal("CI_HEALTH_FIXTURE_NODE must explicitly name a trusted canonical absolute executable")
	}
	canonical, err := filepath.EvalSymlinks(node)
	if err != nil || canonical != node {
		t.Fatal("the explicitly selected Node executable must not use symlinks")
	}
	info, err := os.Lstat(node)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 ||
		info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
		t.Fatal("the explicitly selected Node executable is not a regular unprivileged executable")
	}

	temp := t.TempDir()
	if err := os.Symlink(filepath.Join(web, "node_modules"), filepath.Join(temp, "node_modules")); err != nil {
		t.Fatal("cannot isolate Playwright fixture dependencies")
	}
	for name, data := range map[string]string{
		"playwright.config.mjs": `import { defineConfig } from "@playwright/test";
export default defineConfig({
  testDir: ".", testMatch: "synthetic.spec.mjs", workers: 1, retries: 0,
  reporter: [["junit", {outputFile: "actual.xml"}]], outputDir: "generated",
});
`,
		"synthetic.spec.mjs": `import { expect, test } from "@playwright/test";
test("fixture-case-pass", () => {process.stdout.write("FIXTURE-ONLY-OUTPUT\n");expect(2 + 2).toBe(4);});
test("fixture-case-fail", () => {expect("FIXTURE-ONLY-ACTUAL").toBe("FIXTURE-ONLY-EXPECTED");});
test.skip("fixture-case-skip", () => {});
`,
	} {
		if err := os.WriteFile(filepath.Join(temp, name), []byte(data), 0600); err != nil {
			t.Fatal("cannot stage synthetic Playwright fixture")
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, filepath.Join(web, "node_modules/playwright/cli.js"), "test", "--config", "playwright.config.mjs")
	cmd.Dir = temp
	cmd.Env = []string{"HOME=" + temp, "TMPDIR=" + temp, "CI=1", "FORCE_COLOR=0"}
	var exit *exec.ExitError
	if err := cmd.Run(); !errors.As(err, &exit) || exit.ExitCode() != 1 || ctx.Err() != nil {
		t.Fatal("Playwright did not complete the expected deliberate fixture failure")
	}
	file, err := os.Open(filepath.Join(temp, "actual.xml"))
	if err != nil {
		t.Fatal("Playwright did not produce a JUnit report")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, MaxReportBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > MaxReportBytes ||
		!bytes.Contains(raw, []byte("FIXTURE-ONLY-OUTPUT")) || !bytes.Contains(raw, []byte("FIXTURE-ONLY-EXPECTED")) {
		t.Fatal("actual JUnit report lacks expected fixture diagnostics or is outside the bounded contract")
	}

	const entry = "synthetic.xml"
	archive := buildJUnitZIP(t, fixtureZIPEntry(entry, raw))
	review := zipFixtureReview(archive, entry, raw)
	report, err := ParseReviewedJUnitZIP(archive, review)
	if err != nil {
		t.Fatal("bounded consumer rejected unmodified Playwright JUnit")
	}
	want := Totals{Cases: 3, Passed: 1, Failed: 1, Skipped: 1}
	if summary := report.Summary(); summary.Totals != want || summary.Timed != 2 {
		t.Fatalf("wrong fixed report summary: %+v", summary)
	}
	got := report.Cases()
	if len(got) != 3 || got[0].Outcome != Pass || got[1].Outcome != Failure || got[2].Outcome != Skipped ||
		!got[0].Timed || !got[1].Timed || got[2].Timed {
		t.Fatalf("wrong curated outcomes: %+v", got)
	}
	encoded, err := json.Marshal(got)
	for _, marker := range []string{"FIXTURE-ONLY-", "fixture-case-", "synthetic.spec.mjs", "error-context.md"} {
		if err != nil || strings.Contains(string(encoded), marker) {
			t.Fatal("fixture diagnostic/name/path escaped projection")
		}
	}
	review.EntrySHA256[0] ^= 1
	expectJUnitZIPRefusal(t, archive, review)

	// Export only the known synthetic fixture, after the compatibility and
	// diagnostic-redaction assertions pass. The default local test exports nothing.
	if output := os.Getenv("CI_HEALTH_SYNTHETIC_REPORT"); output != "" {
		if !filepath.IsAbs(output) || filepath.Clean(output) != output || filepath.Base(output) != "synthetic.xml" {
			t.Fatal("synthetic export must be a clean absolute path ending in synthetic.xml")
		}
		out, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal("cannot create a new synthetic report export")
		}
		_, writeErr := out.Write(raw)
		closeErr := out.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatal("cannot complete the synthetic report export")
		}
	}
}
