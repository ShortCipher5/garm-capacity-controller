package main

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/adminview"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeplanning"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	"strings"
)

type nativePlanningConfiguration struct {
	TokenFile          string                            `json:"token_file"`
	SelectionDirectory string                            `json:"selection_directory"`
	Bindings           map[string]nativeplanning.Binding `json:"bindings"`
}

func loadNativePlanning(c *adminConfiguration) (string, error) {
	if c.NativePlanning == nil || !c.EnableSSHInventory || len(c.NativePlanning.Bindings) < 1 || len(c.NativePlanning.Bindings) > 8 || nativeplanning.ValidateDirectory(c.NativePlanning.SelectionDirectory) != nil {
		return "", adminview.ErrConfiguration
	}
	for _, b := range c.NativePlanning.Bindings {
		if nativeplanning.ValidateBinding(b) != nil {
			return "", adminview.ErrConfiguration
		}
	}
	raw, e := securefiles.Read(c.NativePlanning.TokenFile, 256)
	if e != nil {
		return "", adminview.ErrConfiguration
	}
	return strings.TrimSpace(string(raw)), nil
}
