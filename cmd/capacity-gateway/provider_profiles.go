package main

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gatewayrpc"
	"github.com/ShortCipher5/garm-capacity-controller/internal/profilesnapshot"
	"net/http"
)

func validateProviderProfileRole(c configuration, server tls.Certificate) error {
	pin := c.ProviderInspectionFingerprint
	if pin == "" {
		return nil
	}
	if !profilesnapshot.Hex(pin) || len(server.Certificate) == 0 {
		return gatewayrpc.ErrConfiguration
	}
	sum := sha256.Sum256(server.Certificate[0])
	if pin == hex.EncodeToString(sum[:]) {
		return gatewayrpc.ErrConfiguration
	}
	for _, list := range [][]string{c.ProviderFingerprints, c.SourceFingerprints} {
		for _, used := range list {
			if pin == used {
				return gatewayrpc.ErrConfiguration
			}
		}
	}
	for _, scope := range c.StockProviderScopes {
		if pin == scope.ClientFingerprint {
			return gatewayrpc.ErrConfiguration
		}
	}
	return nil
}
func providerProfileHandler(c configuration, next http.Handler, store *admission.SQLiteStore, server tls.Certificate) (http.Handler, error) {
	if validateProviderProfileRole(c, server) != nil {
		return nil, gatewayrpc.ErrConfiguration
	}
	if c.ProviderInspectionFingerprint == "" {
		return next, nil
	}
	return gatewayrpc.NewProfileSnapshotHandler(next, store, c.Controller, c.ProviderInspectionFingerprint)
}
