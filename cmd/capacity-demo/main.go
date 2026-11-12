// capacity-demo serves a loopback-only, authenticated, read-only fixture API.
package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/demo"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "literal loopback address only")
	assets := flag.String("assets", "web/dist", "trusted built demo assets")
	flag.Parse()
	host, _, err := net.SplitHostPort(*listen)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		fmt.Fprintln(os.Stderr, "demo requires a literal loopback listen address")
		os.Exit(1)
	}
	handler, err := demo.NewHandler(os.Getenv("CAPACITY_DEMO_TOKEN"), os.DirFS(*assets))
	if err != nil {
		fmt.Fprintln(os.Stderr, "set CAPACITY_DEMO_TOKEN to a disposable token of at least 32 characters")
		os.Exit(1)
	}
	server := &http.Server{
		Addr:              *listen,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    8192,
	}
	fmt.Fprintln(os.Stderr, "read-only simulation demo listening on loopback; no live integrations")
	if err := server.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "demo server stopped")
		os.Exit(1)
	}
}
