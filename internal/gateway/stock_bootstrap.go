package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"

	common "github.com/cloudbase/garm-provider-common/params"
)

// Exact Linux wrapper from stock GARM 154638445c3949c1958b01812f69d9a1e4d82684,
// internal/templates/userdata/linux_wrapper.tmpl (Apache-2.0). It is compared,
// never executed. The stock source renders []byte, encoded as base64 in JSON.
const pinnedStockLinuxWrapper = `#!/bin/bash

set -ex
set -o pipefail

METADATA_URL="{{ .MetadataURL }}"
BEARER_TOKEN="{{ .CallbackToken }}"

curl -H "Authorization: Bearer $BEARER_TOKEN" --retry 5 --retry-delay 5 --retry-connrefused --fail $METADATA_URL/install-script/ -o /tmp/real-install.sh
chmod +x /tmp/real-install.sh

/tmp/real-install.sh
rm -f /tmp/real-install.sh
`

func expectedStockWrapper(b common.BootstrapInstance) []byte {
	return []byte(strings.NewReplacer("{{ .MetadataURL }}", b.MetadataURL, "{{ .CallbackToken }}", b.InstanceToken).Replace(pinnedStockLinuxWrapper))
}
func shellLiteral(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

// normalizeStockBootstrap accepts exactly the pinned stock-generated default,
// not an arbitrary script. Render an equivalent server-owned wrapper without
// xtrace and with quoted arguments, so bearer tokens are not shell-traced.
func normalizeStockBootstrap(b common.BootstrapInstance) (common.BootstrapInstance, string, error) {
	if len(b.ExtraSpecs) == 0 || bytes.Equal(bytes.TrimSpace(b.ExtraSpecs), []byte("{}")) {
		return b, "provider-default-cloudinit/v1", nil
	}
	decoder := json.NewDecoder(bytes.NewReader(b.ExtraSpecs))
	opening, e := decoder.Token()
	if e != nil || opening != json.Delim('{') {
		return b, "", ErrPolicy
	}
	key, e := decoder.Token()
	if e != nil || key != "runner_install_template" {
		return b, "", ErrPolicy
	}
	var wrapper []byte
	if decoder.Decode(&wrapper) != nil {
		return b, "", ErrPolicy
	}
	closing, e := decoder.Token()
	if e != nil || closing != json.Delim('}') {
		return b, "", ErrPolicy
	}
	if _, e = decoder.Token(); e != io.EOF {
		return b, "", ErrPolicy
	}
	if !bytes.Equal(wrapper, expectedStockWrapper(b)) || b.OSType != common.Linux {
		return b, "", ErrPolicy
	}
	if b.InstanceToken == "" || len(b.InstanceToken) > 16384 || strings.IndexFunc(b.InstanceToken, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._~-", r))
	}) >= 0 || strings.ContainsAny(b.MetadataURL, "{}") {
		return b, "", ErrPolicy
	}
	safe := "#!/bin/bash\nset +x\nset -e\nset -o pipefail\numask 077\nWORK_DIR=$(mktemp -d)\ntrap 'rm -rf -- \"$WORK_DIR\"' EXIT\nMETADATA_URL=" + shellLiteral(b.MetadataURL) + "\nBEARER_TOKEN=" + shellLiteral(b.InstanceToken) + "\ncurl -H \"Authorization: Bearer $BEARER_TOKEN\" --retry 5 --retry-delay 5 --retry-connrefused --fail \"$METADATA_URL/install-script/\" -o \"$WORK_DIR/install.sh\"\nchmod 700 \"$WORK_DIR/install.sh\"\n\"$WORK_DIR/install.sh\"\n"

	data, e := json.Marshal(struct {
		Wrapper []byte `json:"runner_install_template"`
	}{[]byte(safe)})
	if e != nil {
		return b, "", ErrPolicy
	}
	b.ExtraSpecs = data
	return b, "stock-154638445-linux-wrapper/safe-v1", nil
}
