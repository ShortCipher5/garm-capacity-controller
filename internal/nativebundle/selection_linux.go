//go:build linux

package nativebundle

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeplanning"
)

// readSelectionJSON accepts protected deployment configuration, not browser
// evidence or a capability. Duplicate and unknown fields fail closed.
func readSelectionJSON(path string, out any) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || protectedDirectory(filepath.Dir(path)) != nil {
		return ErrBundle
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return ErrBundle
	}
	defer root.Close()
	raw, err := read(root, filepath.Base(path), 64<<10, true)
	if err != nil {
		return ErrBundle
	}
	defer clear(raw)
	if _, err = decodeObject(raw); err != nil {
		return ErrBundle
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(out) != nil || dec.Decode(new(any)) != io.EOF {
		return ErrBundle
	}
	return nil
}

func applySelection(config map[string]any, c Configuration) (string, error) {
	if c.SelectionFile == "" && c.ExpectedBindingFile == "" && c.ExpectedSelectionDigest == "" {
		return "", nil
	}
	if c.SelectionFile == "" || c.ExpectedBindingFile == "" {
		return "", ErrBundle
	}
	var selection nativeplanning.Selection
	var expected nativeplanning.Binding
	if readSelectionJSON(c.SelectionFile, &selection) != nil || readSelectionJSON(c.ExpectedBindingFile, &expected) != nil ||
		nativeplanning.ValidateBinding(expected) != nil || nativeplanning.ValidateFreshSelection(selection, time.Now()) != nil ||
		(c.ExpectedSelectionDigest != "" && (!hash(c.ExpectedSelectionDigest) || nativeplanning.Digest(selection) != c.ExpectedSelectionDigest)) ||
		expected.PreparationID != c.ID || nativeplanning.Digest(expected) != nativeplanning.Digest(selection.Binding) {
		return "", ErrBundle
	}
	control, err := object(config, "control")
	if err != nil {
		return "", ErrBundle
	}
	policy, err := object(control, "policy")
	if err != nil || get(policy, "host") != expected.Host {
		return "", ErrBundle
	}
	// Only these configuration values come from OS discovery. Storage, images,
	// networking, identities and all runtime qualification remain untouched.
	set(config, "limits", selection.Limits)
	set(config, "partition", selection.Partition)
	return nativeplanning.Digest(selection), nil
}
