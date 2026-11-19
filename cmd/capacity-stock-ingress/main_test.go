package main

import (
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockingress"
	"path/filepath"
	"testing"
)

func TestMissingExplicitIngressConfigurationFailsBeforeServing(t *testing.T) {
	for _, args := range [][]string{nil, {"one", "two"}, {filepath.Join(t.TempDir(), "absent.json")}} {
		if err := run(args); !errors.Is(err, stockingress.ErrConfiguration) {
			t.Fatalf("missing explicit configuration accepted: %v", err)
		}
	}
}
