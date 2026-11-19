//go:build linux

package nativecontrol

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeenforcement"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"net/http"
	"time"
)

func serveEnforcement(w http.ResponseWriter, r *http.Request, api *nativeapi.Client, serverLeaf, ownerLeaf string) {
	nonce := r.URL.Query().Get("nonce")
	if r.Method != http.MethodGet || !hash(nonce) || r.URL.RawQuery != "nonce="+nonce || r.URL.RawPath != "" {
		http.Error(w, "exact nonce GET required", 400)
		return
	}
	began := time.Now()
	d, e := api.Enforcement(r.Context())
	if e != nil {
		http.Error(w, "qualified enforcement unavailable", 503)
		return
	}
	i, e := api.Identity(r.Context())
	if e != nil || api.CheckBoundary(r.Context()) != nil || time.Since(began) > 5*time.Second {
		http.Error(w, "qualified identity unavailable", 503)
		return
	}
	d.Owner = nativeidentity.Descriptor{DomainID: i.DomainID(), PolicyDigest: api.PolicyDigest(), OwnerClientLeaf: ownerLeaf, OwnerServerLeaf: serverLeaf, NativeServerKey: i.ServerKey(), NativeOrigin: i.Origin(), NativeScope: i.Scope(), NativeServerName: i.ServerName(), NativeServerVersion: i.ServerVersion()}
	d.Version = nativeenforcement.Version
	d.Nonce = nonce
	d.IssuedAt = began.UTC()
	respond(w, 200, d)
}
