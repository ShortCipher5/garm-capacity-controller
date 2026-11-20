package incusapi

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Fictional bodies use the v7.0.2 ResponseRaw and single-object metadata shape.
// Digests are fixed fixture values for the documented canonical content, not
// computed by the production comparison or obtained from a deployment.
const projectDeclarationBody = `{
	"type":"sync","status":"Success","status_code":200,"operation":"","error_code":0,"error":"",
	"metadata":{"name":"fixture-runners","description":"","used_by":["/1.0/profiles/fixture-small?project=fixture-runners"],
	"config":{"restricted":"true","features.profiles":"true","limits.instances":"6","limits.containers":"5",
	"limits.virtual-machines":"0","limits.cpu":"12","limits.memory":"24GiB","limits.disk":"120GiB"}}}
`

const profileDeclarationBody = `{
	"type":"sync","status":"Success","status_code":200,"operation":"","error_code":0,"error":"",
	"metadata":{"name":"fixture-small","project":"fixture-runners","description":"","used_by":[],
	"config":{"limits.cpu":"2","limits.memory":"2GiB","limits.memory.enforce":"hard","security.privileged":"false"},
	"devices":{"root":{"type":"disk","path":"/","pool":"fixtures","size":"12GiB"},"eth0":{"type":"nic","network":"fixture-net"}}}}
`

func declarationReviewFixture() incus702DeclarationReview {
	return incus702DeclarationReview{
		project: "fixture-runners", profile: "fixture-small", pool: "fixtures",
		projectSHA256: "c627b7784b5d9e7d739cc4fb6b1caadd7d10bd7884d764fbbeec5cb876863766",
		profileSHA256: "a12fda928aa30c5a7b9b89618f0d31029e50da12a223f7afbe2377f6f1013e6e",
	}
}

func TestIncus702ProjectDeclarationExactFixture(t *testing.T) {
	want := incus702ProjectDeclaration{6, 5, 12, 24 << 30, 120 << 30, 2, 2 << 30, 12 << 30}
	for _, project := range []string{
		projectDeclarationBody,
		strings.Replace(projectDeclarationBody, `"restricted":"true","features.profiles":"true"`, `"features.profiles":"true", "restricted":"true"`, 1),
		strings.Replace(projectDeclarationBody, `"/1.0/profiles/fixture-small?project=fixture-runners"`, "", 1),
	} {
		got, err := compareIncus702ProjectDeclaration([]byte(project), []byte(profileDeclarationBody), declarationReviewFixture())
		if err != nil || got != want {
			t.Fatalf("declarations = %+v, %v; want %+v", got, err, want)
		}
	}
}

func TestIncus702ProjectDeclarationRefusesUnboundWire(t *testing.T) {
	tests := []struct {
		name    string
		profile bool
		old     string
		new     string
	}{
		{"envelope-alias", false, `"type"`, `"Type"`},
		{"envelope-extra", false, `"status":"Success"`, `"status":"Success","unexpected":true`},
		{"envelope-duplicate", false, `"type":"sync"`, `"type":"sync","type":"sync"`},
		{"error", false, `"error_code":0`, `"error_code":403`},
		{"not-success", false, `"status":"Success"`, `"status":"Failure"`},
		{"async", false, `"type":"sync"`, `"type":"async"`},
		{"unknown-metadata", false, `"description":""`, `"description":"","unknown":"value"`},
		{"null-use-list", true, `"used_by":[]`, `"used_by":null`},
		{"null-use-item", true, `"used_by":[]`, `"used_by":[null]`},
		{"unrecognized-link", true, `"used_by":[]`, `"used_by":["https://not-an-inventory-link.invalid/1.0"]`},
		{"project-drift", false, `"name":"fixture-runners"`, `"name":"other-runners"`},
		{"profile-owner-fallback", true, `"project":"fixture-runners"`, `"project":"default"`},
		{"profile-owner-missing", true, `"project":"fixture-runners",`, ""},
		{"profile-name-drift", true, `"name":"fixture-small"`, `"name":"fixture-big"`},
		{"config-drift", false, `"limits.cpu":"12"`, `"limits.cpu":"13"`},
		{"device-drift", true, `"size":"12GiB"`, `"size":"10GiB"`},
		{"non-limit-config-drift", true, `"security.privileged":"false"`, `"security.privileged":"true"`},
		{"non-root-device-drift", true, `"fixture-net"`, `"other-net"`},
		{"nested-duplicate", true, `"size":"12GiB"`, `"size":"12GiB","size":"12GiB"`},
		{"nested-null", true, `"size":"12GiB"`, `"size":null`},
		{"device-wrong-shape", true, `{"type":"nic","network":"fixture-net"}`, `["nic"]`},
		{"malformed-surrogate", true, `"fixture-net"`, `"\ud800"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			project, profile := projectDeclarationBody, profileDeclarationBody
			if tc.profile {
				profile = strings.Replace(profile, tc.old, tc.new, 1)
			} else {
				project = strings.Replace(project, tc.old, tc.new, 1)
			}
			if project == projectDeclarationBody && profile == profileDeclarationBody {
				t.Fatal("fixture mutation missing")
			}
			got, err := compareIncus702ProjectDeclaration([]byte(project), []byte(profile), declarationReviewFixture())
			if got != (incus702ProjectDeclaration{}) || !errors.Is(err, ErrResponse) {
				t.Fatalf("got %+v, %v", got, err)
			}
		})
	}
	for _, raw := range []string{projectDeclarationBody + "{}", strings.Repeat(" ", 64<<10) + projectDeclarationBody} {
		if _, err := compareIncus702ProjectDeclaration([]byte(raw), []byte(profileDeclarationBody), declarationReviewFixture()); err != ErrResponse {
			t.Fatal("accepted trailing or excessive wire body")
		}
	}
	for _, modify := range []func(*incus702DeclarationReview){
		func(p *incus702DeclarationReview) { p.project = "default" },
		func(p *incus702DeclarationReview) { p.projectSHA256 = "" },
		func(p *incus702DeclarationReview) { p.profileSHA256 = "not-a-digest" },
	} {
		p := declarationReviewFixture()
		modify(&p)
		if _, err := compareIncus702ProjectDeclaration([]byte(projectDeclarationBody), []byte(profileDeclarationBody), p); err != ErrConfiguration {
			t.Fatal("accepted missing or unsupported expectation")
		}
	}
}

func TestIncus702ProjectDeclarationRefusesUnsupportedPinnedContent(t *testing.T) {
	// Digest the mutant's exact projection to prove refusal even when it matches
	// the fixture's supplied pin. This is test data only, never a review producer.
	for _, tc := range []struct{ old, new string }{
		{`"features.profiles":"true"`, `"features.profiles":"false"`},
		{`"restricted":"true"`, `"restricted":"1"`},
		{`"limits.instances":"6",`, ""},
		{`"limits.containers":"5"`, `"limits.containers":"0"`},
		{`"limits.virtual-machines":"0"`, `"limits.virtual-machines":"1"`},
		{`"limits.memory":"24GiB"`, `"limits.memory":"75%"`},
		{`"limits.disk":"120GiB"`, `"limits.disk":"120GiB","limits.disk.pool.fixtures":"100GiB"`},
		{`"limits.disk":"120GiB"`, `"limits.disk":"9223372036854775807TiB"`},
	} {
		project := strings.Replace(projectDeclarationBody, tc.old, tc.new, 1)
		metadata, err := incus702DeclarationMetadata([]byte(project), "name", "config", "description", "used_by")
		if err != nil {
			t.Fatal(err)
		}
		config, err := incus702DeclarationStringMap(metadata["config"], 128)
		if err != nil {
			t.Fatal(err)
		}
		review := declarationReviewFixture()
		review.projectSHA256 = incus702DeclarationHash("incus-project-config/v7.0.2", struct {
			Name   string            `json:"name"`
			Config map[string]string `json:"config"`
		}{review.project, config})
		if _, err := compareIncus702ProjectDeclaration([]byte(project), []byte(profileDeclarationBody), review); err != ErrResponse {
			t.Fatalf("accepted unsupported pinned content %s", tc.new)
		}
	}
	for _, tc := range []struct{ old, new string }{
		{`"limits.memory.enforce":"hard"`, `"limits.memory.enforce":"soft"`},
		{`"limits.cpu":"2"`, `"limits.cpu":"0-2"`},
		{`"limits.cpu":"2"`, `"limits.cpu":"13"`},
		{`"pool":"fixtures"`, `"pool":"other"`},
		{`"size":"12GiB"`, `"size":"12GiB","source":"/other"`},
		{`"size":"12GiB"`, `"size":"12GiB"},"other":{"type":"disk","path":"/extra"`},
	} {
		profile := strings.Replace(profileDeclarationBody, tc.old, tc.new, 1)
		metadata, err := incus702DeclarationMetadata([]byte(profile), "name", "project", "config", "devices", "description", "used_by")
		if err != nil {
			t.Fatal(err)
		}
		config, err := incus702DeclarationStringMap(metadata["config"], 128)
		if err != nil {
			t.Fatal(err)
		}
		var devices map[string]map[string]string
		if json.Unmarshal(metadata["devices"], &devices) != nil {
			t.Fatal("invalid fixture mutation")
		}
		review := declarationReviewFixture()
		review.profileSHA256 = incus702DeclarationHash("incus-profile-config/v7.0.2", struct {
			Project string                       `json:"project"`
			Name    string                       `json:"name"`
			Config  map[string]string            `json:"config"`
			Devices map[string]map[string]string `json:"devices"`
		}{review.project, review.profile, config, devices})
		if _, err := compareIncus702ProjectDeclaration([]byte(projectDeclarationBody), []byte(profile), review); err != ErrResponse {
			t.Fatalf("accepted unsupported pinned content %s", tc.new)
		}
	}
}

func TestIncus702ProjectDeclarationNumericSubset(t *testing.T) {
	for text, want := range map[string]int64{
		"1024": 1024, "1024B": 1024, "1KiB": 1024, "2MiB": 2 << 20, "3GiB": 3 << 30, "4TiB": 4 << 40,
		"1GB": 0, "1 bytes": 0, "1.5GiB": 0, "00": 0, "0": 0, "+1B": 0, " 1GiB": 0, "1GiB ": 0, "-1": 0, "5%": 0,
		"9223372036854775807": 1<<63 - 1, "9223372036854775808": 0, "8388608TiB": 0,
	} {
		if got := incus702DeclarationBytes(text); got != want {
			t.Errorf("%q = %d; want %d", text, got, want)
		}
	}
}
