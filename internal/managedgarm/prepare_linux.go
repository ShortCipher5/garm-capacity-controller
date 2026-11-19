package managedgarm

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type Preparation struct {
	Directory, GarmImage, ControllerImage, IngressImage string
	OmitIngressPublication                              bool `json:"omit_ingress_publication,omitempty"`
}
type prepared struct {
	OmitIngressPublication                   bool `json:"omit_ingress_publication,omitempty"`
	GarmServerFingerprint                    string
	Version, Deployment, State               string
	GarmImage, ControllerImage, IngressImage string
}
type credential struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Email    string `json:"email"`
	FullName string `json:"full_name"`
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return hex.EncodeToString(b), nil
}
func exclusiveFile(path string, b []byte, uid int) error {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	if _, e = f.Write(b); e != nil {
		return e
	}
	if e = f.Chown(uid, uid); e != nil {
		return e
	}
	return f.Sync()
}

// Prepare creates only new private deployment material. It does not start
// containers, expose ports, initialize accounts, issue tokens or enable writes.
// Existing directories are never adopted, cleared or overwritten.
func Prepare(p Preparation) error {
	if os.Geteuid() != 0 || !filepath.IsAbs(p.Directory) || filepath.Clean(p.Directory) != p.Directory || p.Directory == "/" {
		return ErrBoundary
	}
	for _, v := range []string{p.GarmImage, p.ControllerImage, p.IngressImage} {
		if !strings.HasPrefix(v, "sha256:") || !digest.MatchString(strings.TrimPrefix(v, "sha256:")) {
			return ErrBoundary
		}
	}
	if rootControlledAncestors(filepath.Dir(p.Directory)) != nil {
		return ErrBoundary
	}
	parent, e := os.Lstat(filepath.Dir(p.Directory))
	if e != nil || !parent.IsDir() || parent.Mode().Perm()&0022 != 0 {
		return ErrBoundary
	}
	st, ok := parent.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 {
		return ErrBoundary
	}
	real, e := filepath.EvalSymlinks(filepath.Dir(p.Directory))
	if e != nil || real != filepath.Dir(p.Directory) {
		return ErrBoundary
	}
	if e = os.Mkdir(p.Directory, 0700); e != nil {
		return ErrBoundary
	}
	// Leave any partial directory private for explicit recovery; never silently
	// regenerate credentials or adopt a database after a failed preparation.
	for name, uid := range map[string]int{"garm-config": 65530, "garm-state": 65530, "supervisor-admin": 0, "controller-config": 65532, "controller-state": 65532, "ingress": 65531} {
		d := filepath.Join(p.Directory, name)
		if os.Mkdir(d, 0700) != nil || os.Chown(d, uid, uid) != nil {
			return ErrBoundary
		}
	}
	password, e := randomHex(32)
	if e != nil {
		return ErrBoundary
	}
	jwt, e := randomHex(32)
	if e != nil {
		return ErrBoundary
	}
	dbkey, e := randomHex(16)
	if e != nil {
		return ErrBoundary
	}
	id, e := randomHex(32)
	if e != nil {
		return ErrBoundary
	}
	secret, _ := json.Marshal(credential{Username: "capacitycontroller", Password: password, Email: "capacity-controller@localhost.invalid", FullName: "Managed capacity controller"})
	if exclusiveFile(filepath.Join(p.Directory, "supervisor-admin", "bootstrap-user.json"), secret, 0) != nil {
		return ErrBoundary
	}
	cert, key, serverPin, e := stockTLSMaterial(time.Now().UTC())
	if e != nil {
		return ErrBoundary
	}
	for _, f := range []struct {
		dir, name string
		data      []byte
		uid       int
	}{
		{"garm-config", "api-cert.pem", cert, 65530}, {"garm-config", "api-key.pem", key, 65530},
		{"supervisor-admin", "api-cert.pem", cert, 0}, {"ingress", "api-cert.pem", cert, 65531},
	} {
		if exclusiveFile(filepath.Join(p.Directory, f.dir, f.name), f.data, f.uid) != nil {
			return ErrBoundary
		}
	}
	stock := managedStockConfiguration(jwt, dbkey)
	if exclusiveFile(filepath.Join(p.Directory, "garm-config", "garm.toml"), []byte(stock), 65530) != nil {
		return ErrBoundary
	}
	compose, e := json.MarshalIndent(composeDocument(p, id), "", "  ")
	if e != nil || exclusiveFile(filepath.Join(p.Directory, "compose.json"), compose, 0) != nil {
		return ErrBoundary
	}
	manifest, _ := json.Marshal(preparationMetadata(p, id, serverPin))
	if exclusiveFile(filepath.Join(p.Directory, "preparation.json"), manifest, 0) != nil {
		return ErrBoundary
	}
	dir, e := os.Open(p.Directory)
	if e != nil {
		return ErrBoundary
	}
	defer dir.Close()
	return dir.Sync()
}

// preparationMetadata records intent only; no runtime verifier consumes this field.
func preparationMetadata(p Preparation, id, serverPin string) prepared {
	return prepared{OmitIngressPublication: p.OmitIngressPublication, GarmServerFingerprint: serverPin, Version: "managed-garm/v1", Deployment: id, State: "prepared_not_initialized", GarmImage: p.GarmImage, ControllerImage: p.ControllerImage, IngressImage: p.IngressImage}
}

func composeDocument(p Preparation, id string) map[string]any {
	mount := func(src, dst string, ro bool) map[string]any {
		return map[string]any{"type": "bind", "source": filepath.Join(p.Directory, src), "target": dst, "read_only": ro, "bind": map[string]any{"create_host_path": false}}
	}
	base := func(image, user, role string) map[string]any {
		return map[string]any{"image": image, "entrypoint": []string{}, "working_dir": "/", "environment": []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}, "user": user, "read_only": true, "cap_drop": []string{"ALL"}, "security_opt": []string{"no-new-privileges:true"}, "restart": "no", "pids_limit": 128, "profiles": []string{"managed-control-plane"}, "labels": map[string]string{"capacity.managed-deployment": id, "capacity.role": role}, "tmpfs": []string{"/tmp:rw,noexec,nosuid,nodev,size=64m"}}
	}
	garm := base(p.GarmImage, "65530:65530", "garm")
	garm["command"] = []string{"/usr/bin/garm", "-config", "/etc/garm/garm.toml"}
	if !p.OmitIngressPublication {
		garm["ports"] = []string{"8443:8443"}
	}
	garm["volumes"] = []any{mount("garm-config", "/etc/garm", true), mount("garm-state", "/var/lib/garm", false)}
	controller := base(p.ControllerImage, "65532:65532", "controller")
	controller["network_mode"] = "service:garm"
	controller["command"] = []string{"/usr/local/bin/capacity-gateway", "-config", "/run/controlplane/config/gateway.json"}
	controller["volumes"] = []any{mount("controller-config", "/run/controlplane/config", true), mount("controller-state", "/var/lib/capacity-gateway", false)}
	ingress := base(p.IngressImage, "65531:65531", "ingress")
	ingress["network_mode"] = "service:garm"
	ingress["command"] = []string{"/usr/local/bin/capacity-stock-ingress", "/run/controlplane/ingress/ingress.json"}
	ingress["volumes"] = []any{mount("ingress", "/run/controlplane/ingress", true)}
	return map[string]any{"services": map[string]any{"garm": garm, "controller": controller, "ingress": ingress}}
}
