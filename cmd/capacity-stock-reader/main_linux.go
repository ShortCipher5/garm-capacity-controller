// capacity-stock-reader serves narrowly scoped inventory separately from the gateway.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/ShortCipher5/garm-capacity-controller/internal/managedread"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
)

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: capacity-stock-reader <owner-only-config-file>")
	}
	data, err := securefiles.Read(os.Args[1], 64<<10)
	if err != nil {
		log.Fatal("stock inventory config unavailable")
	}
	config := managedread.PrivateStockConfig{}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&config) != nil || dec.Decode(new(any)) != io.EOF {
		log.Fatal("stock inventory config invalid")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := managedread.ServePrivateStock(ctx, config); err != nil {
		log.Fatal("stock inventory service unavailable")
	}
}
