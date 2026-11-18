package managedwriter

import (
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedgarm"
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedsource"
	"net/http"
	"time"
)

// SetSourceInspector is startup-only. The separate role cannot use demand,
// credentials or pause endpoints; the writer command validates actual key separation.
func (h *Handler) SetSourceInspector(leaf string) error {
	raw, e := hex.DecodeString(leaf)
	if h == nil || h.inspector != nil || e != nil || len(raw) != 32 || hex.EncodeToString(raw) != leaf || subtle.ConstantTimeCompare(raw, h.leaf[:]) == 1 {
		return managedgarm.ErrBoundary
	}
	v := [32]byte{}
	copy(v[:], raw)
	h.inspector = &v
	return nil
}
func (h *Handler) serveSource(w http.ResponseWriter, r *http.Request, leaf [32]byte) {
	fail := func() { http.Error(w, "managed source unavailable", http.StatusForbidden) }
	nonce := r.Header.Get(managedsource.NonceHeader)
	raw, e := hex.DecodeString(nonce)
	if r.Method != http.MethodGet || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) != 0 || h.inspector == nil || subtle.ConstantTimeCompare(leaf[:], h.inspector[:]) != 1 || len(r.Header.Values(managedsource.NonceHeader)) != 1 || e != nil || len(raw) != 32 || hex.EncodeToString(raw) != nonce {
		fail()
		return
	}
	if h.session == nil || h.session.Verify(r.Context()) != nil {
		http.Error(w, "managed source unavailable", 503)
		return
	}
	source, controller := h.session.Identity(), h.session.ControllerID()
	if source == "" || controller == "" {
		http.Error(w, "managed source unavailable", 503)
		return
	}
	v := struct {
		Version    string    `json:"version"`
		Nonce      string    `json:"nonce"`
		Controller string    `json:"controller"`
		Source     string    `json:"source"`
		ObservedAt time.Time `json:"observed_at"`
	}{"managed-source/v1", nonce, controller, source, time.Now().UTC()}
	value, e := json.Marshal(v)
	if e != nil {
		http.Error(w, "managed source unavailable", 503)
		return
	}
	w.Header().Set(managedsource.SourceHeader, source)
	w.Header().Set(managedsource.ControllerHeader, controller)
	w.Write(value)
}
