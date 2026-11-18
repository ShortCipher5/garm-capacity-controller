//go:build linux

package nativecontrol

import (
	"context"
	"net/http"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeartifacts"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
)

// serveArtifacts is reached only after the execution server's exact controller
// mTLS check. It observes the retained construction root, not request paths or
// configuration claims. No mutation, charge release or adoption is performed.
func serveArtifacts(w http.ResponseWriter, r *http.Request, api *nativeapi.Client, serverLeaf, ownerLeaf string) {
	w.Header().Set("Cache-Control", "no-store")
	nonce := r.URL.Query().Get("nonce")
	if r.Method != http.MethodGet || r.URL.Path != nativeartifacts.Path || !hash(nonce) || r.URL.RawQuery != "nonce="+nonce || r.URL.RawPath != "" {
		http.Error(w, "exact nonce GET required", 400)
		return
	}
	began := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	root, e := api.ArtifactRoot(ctx)
	if e != nil {
		http.Error(w, "qualified artifact root unavailable", 503)
		return
	}
	i, e := api.Identity(ctx)
	if e != nil || !i.Valid() || api.CheckBoundary(ctx) != nil || ctx.Err() != nil || !root.Fresh() || time.Since(began) > 5*time.Second {
		http.Error(w, "qualified identity unavailable", 503)
		return
	}
	d := nativeartifacts.Descriptor{
		Version: nativeartifacts.Version, Nonce: nonce, IssuedAt: began.UTC(),
		Owner:      nativeidentity.Descriptor{DomainID: i.DomainID(), PolicyDigest: api.PolicyDigest(), OwnerClientLeaf: ownerLeaf, OwnerServerLeaf: serverLeaf, NativeServerKey: i.ServerKey(), NativeOrigin: i.Origin(), NativeScope: i.Scope(), NativeServerName: i.ServerName(), NativeServerVersion: i.ServerVersion()},
		ArtifactID: root.ID(), PoolGUID: root.PoolGUID(), PoolLoadGUID: root.PoolLoadGUID(), RootGUID: root.RootGUID(), Dataset: root.Dataset(), QuotaBytes: root.QuotaBytes(),
	}
	if nativeartifacts.ValidateDescriptor(d) != nil {
		http.Error(w, "qualified artifact descriptor unavailable", 503)
		return
	}
	respond(w, 200, d)
}
