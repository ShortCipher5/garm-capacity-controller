package main

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/adminview"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	"github.com/ShortCipher5/garm-capacity-controller/internal/sshinventory"
	"strings"
)

type sshInventoryConfiguration struct {
	TokenFile string                         `json:"token_file"`
	Keys      map[string]string              `json:"credential_files"`
	Targets   map[string]sshinventory.Config `json:"targets"`
}

func newSSHInventory(c *sshInventoryConfiguration, store *admission.SQLiteStore) (*sshinventory.Service, string, error) {
	if c == nil || len(c.Keys) == 0 || len(c.Keys) > 32 {
		return nil, "", adminview.ErrConfiguration
	}
	token, e := securefiles.Read(c.TokenFile, 256)
	if e != nil {
		return nil, "", adminview.ErrConfiguration
	}
	resolver := sshinventory.PrivateKeyFiles(c.Keys)
	for _, target := range c.Targets {
		if !admission.ValidOnboardingProfileID(target.CredentialRef) {
			return nil, "", adminview.ErrConfiguration
		}
		if _, e := resolver(context.Background(), target.CredentialRef); e != nil {
			return nil, "", adminview.ErrConfiguration
		}
	}
	audit := func(ctx context.Context, request, target, digest, phase string, available bool) error {
		if store == nil {
			return adminview.ErrConfiguration
		}
		return store.RecordSSHInventoryAudit(ctx, request, target, digest, phase, available)
	}
	service, e := sshinventory.NewService(c.Targets, resolver, audit)
	if e != nil {
		return nil, "", adminview.ErrConfiguration
	}
	return service, strings.TrimSpace(string(token)), nil
}
