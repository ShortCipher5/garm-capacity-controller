// capacity-sim is an offline demonstration. It never invokes external APIs.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
)

func main() {
	path := flag.String("ledger", "demo.ledger.json", "disposable local simulation ledger")
	key := flag.String("key", "", "stable simulation request identity (required)")
	pool := flag.String("pool", "example/app", "synthetic pool")
	host := flag.String("host", "host-a", "synthetic host")
	flag.Parse()
	budget := admission.Resources{CPU: 6000, RAM: 12288, Runners: 6}
	config := admission.Config{
		Global:            budget,
		MaxObservationAge: 5 * time.Minute,
		Hosts: map[string]admission.Host{
			"host-a": {Budget: admission.Resources{CPU: 4000, RAM: 8192, Runners: 4}, Mode: "enabled", Healthy: true, ObservedAt: time.Now()},
			"host-b": {Budget: admission.Resources{CPU: 4000, RAM: 8192, Runners: 4}, Mode: "draining", Healthy: true, ObservedAt: time.Now()},
		},
		Pools: map[string]admission.Pool{
			"example/app": {Budget: budget, Hosts: []string{"host-a", "host-b"}},
			"example/api": {Budget: budget, Hosts: []string{"host-a", "host-b"}},
		},
	}
	store, err := admission.Open(*path, config)
	if err != nil {
		fmt.Fprintln(os.Stderr, "simulation ledger unavailable")
		os.Exit(1)
	}
	decision, err := store.Admit(admission.Request{Key: *key, Pool: *pool, Host: *host, Resources: admission.Resources{CPU: 1000, RAM: 2048, Runners: 1}})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(decision); err != nil {
		os.Exit(1)
	}
}
