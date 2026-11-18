package main

import (
	"bytes"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
)

func inventoryClient(c configuration, provision bool) (*garmapi.Client, []byte, error) {
	if c.Garm.ManagedProxy {
		if c.Garm.TokenFile != "" || c.Garm.ProxyServerKeyPin == "" || provision || len(c.PreparationPools) > 0 || c.Admin != nil && (c.Admin.GarmManagement != nil || c.Admin.EnableExperimentalGarmManagement) && validateManagedSettingsRouting(c) != nil {
			return nil, nil, garmapi.ErrInventory
		}
		trust, err := securefiles.LoadTLS(c.Garm.TLS)
		if err != nil {
			return nil, nil, err
		}
		client, err := garmapi.NewManagedInventory(c.Garm.Endpoint, c.Garm.ProxyServerKeyPin, trust, c.Garm.Providers, c.Garm.ScaleSets)
		return client, nil, err
	}
	if c.Garm.ProxyServerKeyPin != "" {
		return nil, nil, garmapi.ErrInventory
	}
	token, err := securefiles.Read(c.Garm.TokenFile, 16384)
	if err != nil {
		return nil, nil, err
	}
	token = bytes.TrimSpace(token)
	trust, err := securefiles.LoadTLS(c.Garm.TLS)
	if err != nil {
		return nil, nil, err
	}
	client, err := garmapi.New(c.Garm.Endpoint, string(token), trust, c.Garm.Providers, c.Garm.ScaleSets)
	return client, token, err
}
