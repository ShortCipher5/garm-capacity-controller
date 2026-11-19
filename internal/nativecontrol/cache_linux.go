//go:build linux

package nativecontrol

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativecache"
	"net/http"
	"time"
)

const cachePath = "/v1/native/cache"

// This authenticated diagnostic deliberately cannot supply a hard memory ceiling
// or a physical-release proof. It observes the kernel visible to this owner.
func serveCache(w http.ResponseWriter, r *http.Request, api *nativeapi.Client) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet || r.URL.Path != cachePath || r.URL.RawQuery != "" || r.URL.RawPath != "" {
		http.Error(w, "exact diagnostic GET required", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if api == nil || api.CheckBoundary(ctx) != nil {
		http.Error(w, "qualified owner unavailable", 503)
		return
	}
	sample, e := nativecache.Observe(ctx)
	if e != nil || api.CheckBoundary(ctx) != nil || ctx.Err() != nil || !sample.Fresh() {
		http.Error(w, "fresh cache diagnostics unavailable", 503)
		return
	}
	respond(w, 200, sample.Snapshot())
}
