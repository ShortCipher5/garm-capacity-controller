//go:build linux && stock_read_pilot_harness

package managedgarm

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func pilotStageJSONFixture(t *testing.T) ([]byte, []byte, time.Time) {
	t.Helper()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	review := stockPilotPrelaunchReview{"stock-pilot-prelaunch/v1", now.Add(30 * time.Minute).Format(time.RFC3339), "sha256:" + strings.Repeat("1", 64), "sha256:" + strings.Repeat("2", 64), strings.Repeat("3", 64), strings.Repeat("4", 64), strings.Repeat("5", 64), strings.Repeat("6", 64), strings.Repeat("7", 64), strings.Repeat("8", 64)}
	encoded, _ := json.Marshal(review)
	var keyed map[string]any
	if json.Unmarshal(encoded, &keyed) != nil {
		t.Fatal("fixture")
	}
	// Dictionary order, spaces/newlines are intentionally unlike Go's struct
	// encoding, as with Python json.dumps(sort_keys=True, indent=2).
	raw, _ := json.MarshalIndent(keyed, "", "  ")
	files := map[string]string{}
	for _, name := range stockPilotPreparedNames() {
		files[name] = strings.Repeat("9", 64)
	}
	files["prelaunch.json"] = stockBindingDigest(raw)
	files["stock-api.der"] = review.StockLeafSHA256
	files["gateway-api.der"] = review.GatewayLeafSHA256
	files["ingress-api.der"] = review.IngressLeafSHA256
	manifest := stockPilotPreparedManifest{"stock-pilot-prepared/v1", review, stockBindingDigest(raw), files, strings.Repeat("a", 64)}
	prepared, _ := json.MarshalIndent(manifest, "", " ")
	return raw, prepared, now
}
func TestStockPilotStageJSONWhitespaceOrderingAndRawCommitment(t *testing.T) {
	raw, prepared, now := pilotStageJSONFixture(t)
	original := bytes.Clone(raw)
	review, e := decodeStockPilotPrelaunch(raw, now)
	if e != nil {
		t.Fatal(e)
	}
	out, e := decodeStockPilotPrepared(prepared, raw, now)
	if e != nil {
		t.Fatal(e)
	}
	if out.Review != review || out.ReviewSHA256 != stockBindingDigest(original) || !bytes.Equal(raw, original) {
		t.Fatal("raw commitment changed")
	}
	canonical, _ := json.Marshal(review)
	if bytes.Equal(raw, canonical) || stockBindingDigest(canonical) == out.ReviewSHA256 {
		t.Fatal("fixture does not distinguish encodings")
	}
	if _, e := decodeStockPilotPrepared(prepared, canonical, now); e == nil {
		t.Fatal("accepted different raw commitment")
	}
	var authenticated stockPilotPrelaunchReview
	if stockBindingJSON(raw, &authenticated) {
		t.Fatal("authenticated parser was loosened")
	}
	// The schema decoder does not convert staged raw bytes into runtime records.
	if _, e := decodeStockPilotPrepared(append(prepared, []byte(" {}")...), raw, now); e == nil {
		t.Fatal("trailing value")
	}
}
func TestStockPilotStageJSONPrelaunchRefusals(t *testing.T) {
	raw, _, now := pilotStageJSONFixture(t)
	for name, bad := range map[string][]byte{
		"empty": nil, "null": []byte("null"), "array": []byte("[]"), "oversize": bytes.Repeat([]byte(" "), 16385),
		"unknown":           bytes.Replace(raw, []byte("{\n"), []byte("{\n\"Unknown\":1,"), 1),
		"duplicate":         bytes.Replace(raw, []byte("{\n"), []byte("{\n\"Version\":\"stock-pilot-prelaunch/v1\","), 1),
		"escaped duplicate": bytes.Replace(raw, []byte("{\n"), []byte("{\n\"\\u0056ersion\":\"stock-pilot-prelaunch/v1\","), 1),
		"case alias":        bytes.Replace(raw, []byte("\"Version\""), []byte("\"version\""), 1),
		"wrong type":        bytes.Replace(raw, []byte("\"stock-pilot-prelaunch/v1\""), []byte("true"), 1),
		"null required":     bytes.Replace(raw, []byte("\"stock-pilot-prelaunch/v1\""), []byte("null"), 1),
		"truncated":         raw[:len(raw)-1], "invalid utf8": append([]byte{'{', 0xff}, raw[1:]...),
		"trailing": append(bytes.Clone(raw), '0'),
	} {
		t.Run(name, func(t *testing.T) {
			if _, e := decodeStockPilotPrelaunch(bad, now); e == nil {
				t.Fatal("accepted malformed stage")
			}
		})
	}
	var fields map[string]any
	json.Unmarshal(raw, &fields)
	for key, value := range map[string]any{"Version": "other", "ExpiresAt": now.Format(time.RFC3339), "GatewayImage": stockReadImage, "IngressImage": fields["GatewayImage"], "GatewayExecutableSHA256": strings.Repeat("A", 64), "CredentialDigest": -1} {
		copy := map[string]any{}
		for k, v := range fields {
			copy[k] = v
		}
		copy[key] = value
		bad, _ := json.Marshal(copy)
		if _, e := decodeStockPilotPrelaunch(bad, now); e == nil {
			t.Fatalf("accepted %s", key)
		}
	}
	if _, e := decodeStockPilotPrelaunch(raw, now.Add(-2*time.Hour)); e == nil {
		t.Fatal("unbounded future expiry")
	}
	if _, e := decodeStockPilotPrelaunch(raw, time.Time{}); e == nil {
		t.Fatal("missing clock")
	}
}
func TestStockPilotStageJSONPreparedNestedRefusals(t *testing.T) {
	raw, prepared, now := pilotStageJSONFixture(t)
	for name, change := range map[string]func(map[string]any){
		"unknown":           func(m map[string]any) { m["other"] = true },
		"nested unknown":    func(m map[string]any) { m["Review"].(map[string]any)["other"] = true },
		"nested null":       func(m map[string]any) { m["Review"] = nil },
		"missing file":      func(m map[string]any) { delete(m["Files"].(map[string]any), "stock-api.der") },
		"foreign file":      func(m map[string]any) { m["Files"].(map[string]any)["../x"] = strings.Repeat("a", 64) },
		"file wrong type":   func(m map[string]any) { m["Files"].(map[string]any)["stock-api.der"] = 10 },
		"file wrong digest": func(m map[string]any) { m["Files"].(map[string]any)["stock-api.der"] = strings.Repeat("a", 64) },
		"review mismatch":   func(m map[string]any) { m["Review"].(map[string]any)["CredentialDigest"] = strings.Repeat("a", 64) },
		"wrong spki":        func(m map[string]any) { m["ServerSPKI"] = strings.Repeat("a", 63) },
		"digest null":       func(m map[string]any) { m["ReviewSHA256"] = nil },
	} {
		t.Run(name, func(t *testing.T) {
			var m map[string]any
			json.Unmarshal(prepared, &m)
			change(m)
			bad, _ := json.Marshal(m)
			if _, e := decodeStockPilotPrepared(bad, raw, now); e == nil {
				t.Fatal("malformed prepared accepted")
			}
		})
	}
	for _, replacement := range []struct{ old, new string }{
		{`"Files": {`, `"Files": {"stock-api.der":"` + strings.Repeat("5", 64) + `",`},
		{`"Review": {`, `"Review": {"Version":"stock-pilot-prelaunch/v1",`},
	} {
		bad := bytes.Replace(prepared, []byte(replacement.old), []byte(replacement.new), 1)
		if bytes.Equal(bad, prepared) {
			t.Fatal("mutation not applied")
		}
		if _, e := decodeStockPilotPrepared(bad, raw, now); e == nil {
			t.Fatal("nested duplicate accepted")
		}
	}
}
func TestStockPilotFixtureCredentialNormalizationUsesExistingRenderer(t *testing.T) {
	p, _ := stockReadFixture()
	p.Directory = stockPilotHarnessDirectory
	_, auth := stockAuthFixture(t)
	var c credential
	if !stockBindingJSON(auth[4], &c) {
		t.Fatal("fixture")
	}
	// Exact Python default json.dumps spacing and a deliberately different key order.
	raw := []byte(`{"full_name": "` + c.FullName + `", "email": "` + c.Email + `", "password": "` + c.Password + `", "username": "` + c.Username + `"}`)
	before := bytes.Clone(raw)
	rawDigest := stockBindingDigest(raw)
	canonical, e := normalizeStockPilotFixtureCredential(raw)
	if e != nil {
		t.Fatal(e)
	}
	if credentialHash(c) != stockBindingDigest(canonical) || credentialHash(c) == rawDigest {
		t.Fatal("canonical semantic credential digest confused with raw stage digest")
	}
	if !bytes.Equal(canonical, auth[4]) || !bytes.Equal(raw, before) || stockBindingDigest(raw) != rawDigest || stockBindingDigest(canonical) == rawDigest {
		t.Fatal("raw/canonical distinction lost")
	}
	files, e := stockPilotFixtureProvisional(p, auth[1], canonical)
	if e != nil {
		t.Fatal(e)
	}
	for _, f := range files {
		clear(f.Bytes)
	}
	completed, e := stockPilotFixtureCompleted(p, auth[1], canonical, auth[2], auth[3])
	if e != nil {
		t.Fatal(e)
	}
	clear(completed.Bytes)
	var decoded credential
	if stockBindingJSON(raw, &decoded) {
		t.Fatal("runtime parser accepted fixture wire")
	}
	for _, bad := range [][]byte{
		[]byte(`{"username":"capacitycontroller"}`), []byte(`null`),
		bytes.Replace(raw, []byte(`"username"`), []byte(`"Username"`), 1),
		bytes.Replace(raw, []byte(`{`), []byte(`{"username":"capacitycontroller",`), 1),
		bytes.Replace(raw, []byte(`{`), []byte(`{"unknown":1,`), 1),
		bytes.Replace(raw, []byte(`"`+c.Password+`"`), []byte(`null`), 1),
	} {
		if _, e := normalizeStockPilotFixtureCredential(bad); e == nil {
			t.Fatal("malformed fixture credential accepted")
		}
	}
	canonical[0] = '!'
	if !bytes.Equal(raw, before) {
		t.Fatal("canonical output aliases input")
	}
}
