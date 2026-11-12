package main

import (
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/adminview"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
)

type adminConfiguration struct {
	Listen    string               `json:"listen"`
	TLS       securefiles.TLSFiles `json:"tls"`
	TokenFile string               `json:"token_file"`
	Assets    string               `json:"assets"`
}

func newAdminServer(config *adminConfiguration, store *admission.SQLiteStore, provision bool) (*http.Server, func(), error) {
	noop := func() {}
	if config == nil {
		return nil, noop, nil
	}
	host, _, err := net.SplitHostPort(config.Listen)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsLoopback() {
		return nil, noop, adminview.ErrConfiguration
	}
	token, err := securefiles.Read(config.TokenFile, 256)
	if err != nil {
		return nil, noop, err
	}
	credentials, err := securefiles.LoadTLS(config.TLS)
	if err != nil {
		return nil, noop, err
	}
	var root *os.Root
	cleanup := noop
	if config.Assets != "" {
		root, err = os.OpenRoot(config.Assets)
		if err != nil {
			return nil, noop, adminview.ErrConfiguration
		}
		cleanup = func() { root.Close() }
	}
	handler, err := adminview.New(store, strings.TrimSpace(string(token)), root, provision)
	if err != nil {
		cleanup()
		return nil, noop, err
	}
	handler.Audit = func(event string) { log.Print(event) }
	return &http.Server{Addr: config.Listen, Handler: handler, TLSConfig: credentials, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10, ErrorLog: log.New(redactedConnectionLog{}, "", 0)}, cleanup, nil
}
