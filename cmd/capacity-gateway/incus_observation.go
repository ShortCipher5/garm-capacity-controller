package main

import (
	"crypto/tls"

	"github.com/ShortCipher5/garm-capacity-controller/internal/gatewayrpc"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
)

func validateIncusObservationMode(c configuration, provision bool) error {
	observed := 0
	for _, host := range c.Hosts {
		if host.ObservationServerKeyPin != "" {
			observed++
		}
	}
	if observed > 0 && (provision || observed != len(c.Hosts) || c.EnableStockProvider || len(c.StockProviderScopes) != 0 || len(c.NativeStock) != 0) {
		return gatewayrpc.ErrConfiguration
	}
	return nil
}

func newConfiguredIncusClient(host hostConfiguration, trust *tls.Config) (*incusapi.Client, error) {
	if host.ObservationServerKeyPin != "" {
		return incusapi.NewObserver(host.Endpoint, host.Project, host.ObservationServerKeyPin, trust)
	}
	return incusapi.New(host.Endpoint, host.Project, trust)
}
