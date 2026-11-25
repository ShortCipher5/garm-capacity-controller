package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedgarm"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	"io"
	"os"
)

func run(args []string) error {
	if len(args) != 2 {
		return managedgarm.ErrBoundary
	}
	if managedgarm.ValidateSupervisorFilePath(args[1]) != nil {
		return managedgarm.ErrBoundary
	}
	b, e := securefiles.Read(args[1], 16384)
	if e != nil {
		return managedgarm.ErrBoundary
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	switch args[0] {
	case "prepare":
		var p managedgarm.Preparation
		if d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF {
			return managedgarm.ErrBoundary
		}
		return managedgarm.Prepare(p)
	case "bootstrap":
		var p managedgarm.Specification
		if d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF {
			return managedgarm.ErrBoundary
		}
		return managedgarm.Bootstrap(context.Background(), p)
	case "inspect-provider":
		var p struct {
			Boundary    managedgarm.Specification
			Profiles    managedgarm.ProviderProfileConfiguration
			Executables managedgarm.ProviderPathPins
		}
		if d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF {
			return managedgarm.ErrBoundary
		}
		session, err := managedgarm.OpenSession(context.Background(), p.Boundary)
		if err != nil {
			return err
		}
		defer session.Close()
		_, err = managedgarm.NewProviderPathObserver(context.Background(), session, p.Profiles, p.Executables)
		return err
	case "inspect-restart-history":
		var p managedgarm.Specification
		if d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF {
			return managedgarm.ErrBoundary
		}
		state, err := managedgarm.InspectRestartHistory(p)
		if err == nil {
			fmt.Println(state)
		}
		return err
	case "inspect":
		var p managedgarm.Specification
		if d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF {
			return managedgarm.ErrBoundary
		}
		_, e := managedgarm.Observe(context.Background(), p)
		return e
	default:
		return managedgarm.ErrBoundary
	}
}
func main() {
	if run(os.Args[1:]) != nil {
		fmt.Fprintln(os.Stderr, "managed boundary unavailable; no writer authorization issued")
		os.Exit(1)
	}
	fmt.Println("managed boundary step completed; writer activation remains disabled")
}
