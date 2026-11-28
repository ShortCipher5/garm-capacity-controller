package main

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/adminview"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incussetup"
	"golang.org/x/crypto/ssh"
)

type incusSetupConfiguration struct {
	TokenFile   string            `json:"token_file"`
	Credentials map[string]string `json:"admin_credential_files"`
	Targets     map[string]struct {
		Target incussetup.Target `json:"target"`
		Alias  string            `json:"admin_credential_alias"`
	} `json:"targets"`
}

func loadIncusSetup(a *adminConfiguration) (map[string]adminview.IncusSetupTarget, string, error) {
	c := a.IncusSetup
	if c == nil || len(c.Targets) < 1 || len(c.Targets) > 8 {
		return nil, "", adminview.ErrConfiguration
	}
	token, resolve, forbidden, e := nativePreparationAccess(a, &nativePreparationConfiguration{TokenFile: c.TokenFile, Credentials: c.Credentials})
	if e != nil {
		return nil, "", e
	}
	out := map[string]adminview.IncusSetupTarget{}
	seen := map[string]bool{}
	for id, entry := range c.Targets {
		t := entry.Target
		k, e := resolve(context.Background(), entry.Alias)
		if !t.Valid() || e != nil || k == nil || forbidden[t.AdminFingerprint] || ssh.FingerprintSHA256(k.PublicKey()) != t.AdminFingerprint || seen[t.HostKey] {
			return nil, "", adminview.ErrConfiguration
		}
		seen[t.HostKey] = true
		out[id] = adminview.IncusSetupTarget{Target: t, Execute: func(ctx context.Context, prev *incussetup.Report) (incussetup.Report, error) {
			k, err := resolve(ctx, entry.Alias)
			if err != nil {
				return incussetup.Report{}, err
			}
			return t.Run(ctx, k, prev)
		}}
	}
	return out, token, nil
}
