package managedgarm

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

func managedStockConfiguration(jwt, dbkey string) string {
	return fmt.Sprintf("[default]\nenable_webhook_management = true\nenable_log_streamer = false\ndebug_server = false\n[logging]\nenable_log_streamer = false\nlog_format = \"json\"\nlog_level = \"info\"\n[metrics]\nenable = false\n[jwt_auth]\nsecret = %q\ntime_to_live = \"24h\"\n[apiserver]\nbind = \"127.0.0.1\"\nport = 9998\nuse_tls = true\n[apiserver.tls]\ncertificate = \"/etc/garm/api-cert.pem\"\nkey = \"/etc/garm/api-key.pem\"\n[apiserver.webui]\nenable = false\n[database]\nbackend = \"sqlite3\"\npassphrase = %q\n[database.sqlite3]\ndb_file = \"/var/lib/garm/garm.db\"\nbusy_timeout_seconds = 5\n", jwt, dbkey)
}

var jwtLine = regexp.MustCompile(`(?m)^secret = "([0-9a-f]{64})"$`)
var dbLine = regexp.MustCompile(`(?m)^passphrase = "([0-9a-f]{32})"$`)

func stockBase(raw []byte) (string, error) {
	j := jwtLine.FindAllSubmatch(raw, -1)
	d := dbLine.FindAllSubmatch(raw, -1)
	if len(j) != 1 || len(d) != 1 {
		return "", ErrBoundary
	}
	return managedStockConfiguration(string(j[0][1]), string(d[0][1])), nil
}
func providerSections(names []string) (string, error) {
	if len(names) == 0 || len(names) > 256 {
		return "", ErrBoundary
	}
	var b strings.Builder
	previous := ""
	for _, name := range names {
		if !providerName.MatchString(name) || name <= previous {
			return "", ErrBoundary
		}
		previous = name
		fmt.Fprintf(&b, "[[provider]]\nname = %q\nprovider_type = \"external\"\ndisable_jit_config = false\n[provider.external]\ninterface_version = \"v0.1.1\"\nprovider_executable = \"/usr/local/bin/capacity-provider\"\nconfig_file = %q\nenvironment_variables = []\n", name, "/etc/garm/provider-"+name+".json")
	}
	return b.String(), nil
}
func validateProviderTOML(raw []byte, names []string) error {
	base, err := stockBase(raw)
	if err != nil {
		return err
	}
	tail, err := providerSections(names)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, []byte(base+tail)) {
		return ErrBoundary
	}
	return nil
}

// RenderManagedProviderConfiguration is a pure preparation helper. It never
// installs a file, starts GARM or grants authority. Only the exact generated
// stock configuration subset is accepted, not arbitrary TOML adoption.
func RenderManagedProviderConfiguration(base []byte, names []string) ([]byte, error) {
	prefix, err := stockBase(base)
	if err != nil || !bytes.Equal(base, []byte(prefix)) {
		return nil, ErrBoundary
	}
	names = append([]string(nil), names...)
	sort.Strings(names)
	tail, err := providerSections(names)
	if err != nil {
		return nil, err
	}
	return []byte(prefix + tail), nil
}
