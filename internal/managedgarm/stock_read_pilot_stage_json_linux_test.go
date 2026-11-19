//go:build linux && stock_read_pilot_harness

package managedgarm

import (
	"bytes"
	"encoding/json"
	"io"
	"time"
	"unicode/utf8"
)

// Stage-only wire schemas from the reviewed fixture preparation contract.
// These are not authenticated runtime records, observations, or install plans.
// Keep their original input bytes for commitments; never hash a re-encoding.
type stockPilotPrelaunchReview struct {
	Version, ExpiresAt                                                           string
	GatewayImage, IngressImage, GatewayExecutableSHA256, IngressExecutableSHA256 string
	StockLeafSHA256, GatewayLeafSHA256, IngressLeafSHA256, CredentialDigest      string
}
type stockPilotPreparedManifest struct {
	Version      string
	Review       stockPilotPrelaunchReview
	ReviewSHA256 string
	Files        map[string]string
	ServerSPKI   string
}

func stockPilotStageObject(raw []byte, names ...string) (map[string]json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > 16384 || !utf8.Valid(raw) {
		return nil, ErrBoundary
	}
	allowed := map[string]bool{}
	for _, name := range names {
		allowed[name] = true
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return nil, ErrBoundary
	}
	out := map[string]json.RawMessage{}
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok || !allowed[key] || out[key] != nil {
			return nil, ErrBoundary
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, ErrBoundary
		}
		out[key] = value
	}
	last, err := d.Token()
	if err != nil || last != json.Delim('}') || d.Decode(new(any)) != io.EOF || len(out) != len(allowed) {
		return nil, ErrBoundary
	}
	return out, nil
}

func decodeStockPilotPrelaunch(raw []byte, now time.Time) (stockPilotPrelaunchReview, error) {
	var out stockPilotPrelaunchReview
	fail := func() (stockPilotPrelaunchReview, error) { return stockPilotPrelaunchReview{}, ErrBoundary }
	_, err := stockPilotStageObject(raw, "Version", "ExpiresAt", "GatewayImage", "IngressImage", "GatewayExecutableSHA256", "IngressExecutableSHA256", "StockLeafSHA256", "GatewayLeafSHA256", "IngressLeafSHA256", "CredentialDigest")
	if err != nil || json.Unmarshal(raw, &out) != nil || now.IsZero() || out.Version != "stock-pilot-prelaunch/v1" {
		return fail()
	}
	expires, err := time.Parse(time.RFC3339, out.ExpiresAt)
	if err != nil || !expires.After(now) || expires.After(now.Add(time.Hour)) {
		return fail()
	}
	for _, value := range []string{out.GatewayExecutableSHA256, out.IngressExecutableSHA256, out.StockLeafSHA256, out.GatewayLeafSHA256, out.IngressLeafSHA256, out.CredentialDigest} {
		if !digest.MatchString(value) {
			return fail()
		}
	}
	for _, image := range []string{out.GatewayImage, out.IngressImage} {
		if len(image) != 71 || image[:7] != "sha256:" || !digest.MatchString(image[7:]) || image == stockReadImage {
			return fail()
		}
	}
	if out.GatewayImage == out.IngressImage {
		return fail()
	}
	return out, nil
}

func stockPilotPreparedNames() []string {
	return []string{"prelaunch.json", "stock-api.der", "gateway-api.der", "ingress-api.der", "bootstrap-user.json", "read-serve-ca.der", "read-serve-server.pem", "read-serve-server.key", "read-serve-client.der", "gateway-read-ca.pem", "gateway-read-client.pem", "gateway-read-client.key", "probe-wrong-client.pem", "probe-wrong-client.key", "stock-launch.json", "gateway-garm-overlay.json"}
}

// This checks only stage schema and available review linkage, not file contents.
// Files["bootstrap-user.json"] hashes original fixture bytes, whereas
// Review.CredentialDigest is existing credentialHash(c), the canonical semantic
// credential hash. They must NOT be equated. A material consumer must check both
// commitments against the supplied credential before invoking any renderer.
func decodeStockPilotPrepared(raw, prelaunchRaw []byte, now time.Time) (stockPilotPreparedManifest, error) {
	fail := func() (stockPilotPreparedManifest, error) { return stockPilotPreparedManifest{}, ErrBoundary }
	fields, err := stockPilotStageObject(raw, "Version", "Review", "ReviewSHA256", "Files", "ServerSPKI")
	if err != nil {
		return fail()
	}
	expected, err := decodeStockPilotPrelaunch(prelaunchRaw, now)
	if err != nil {
		return fail()
	}
	review, err := decodeStockPilotPrelaunch(fields["Review"], now)
	if err != nil || review != expected {
		return fail()
	}
	files, err := stockPilotStageObject(fields["Files"], stockPilotPreparedNames()...)
	if err != nil {
		return fail()
	}
	var out stockPilotPreparedManifest
	if json.Unmarshal(raw, &out) != nil || out.Version != "stock-pilot-prepared/v1" || !digest.MatchString(out.ServerSPKI) || out.ReviewSHA256 != stockBindingDigest(prelaunchRaw) {
		return fail()
	}
	for name := range files {
		if !digest.MatchString(out.Files[name]) {
			return fail()
		}
	}
	if out.Files["prelaunch.json"] != out.ReviewSHA256 || out.Files["stock-api.der"] != review.StockLeafSHA256 || out.Files["gateway-api.der"] != review.GatewayLeafSHA256 || out.Files["ingress-api.der"] != review.IngressLeafSHA256 {
		return fail()
	}
	return out, nil
}

// Only fixture input is normalized. The original bytes remain unchanged for
// stage hashes. The returned owned encoding is the distinct canonical input
// required by existing provisional/completed renderers and runtime validation.
func normalizeStockPilotFixtureCredential(raw []byte) ([]byte, error) {
	if _, err := stockPilotStageObject(raw, "username", "password", "email", "full_name"); err != nil {
		return nil, ErrBoundary
	}
	var c credential
	if json.Unmarshal(raw, &c) != nil || !validBootstrapCredential(c) {
		return nil, ErrBoundary
	}
	canonical, err := json.Marshal(c)
	if err != nil {
		return nil, ErrBoundary
	}
	return canonical, nil
}
