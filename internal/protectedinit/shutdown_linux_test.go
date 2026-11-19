//go:build linux

package protectedinit

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"
)

func notShutdown(t *testing.T, g *shutdownGate) {
	t.Helper()
	select {
	case <-g.done:
		t.Fatal("premature shutdown")
	default:
	}
}
func TestShutdownRequiresFenceAndHalt(t *testing.T) {
	g := newShutdownGate()
	g.receive(syscall.SIGPWR)
	notShutdown(t, g)
	for _, s := range []os.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP} {
		g.receive(s)
	}
	notShutdown(t, g)
	g.fenceCommitted()
	select {
	case <-g.done:
	default:
		t.Fatal("halt not released")
	}
	h := newShutdownGate()
	h.fenceCommitted()
	notShutdown(t, h)
	h.receive(syscall.SIGTERM)
	notShutdown(t, h)
	h.receive(syscall.SIGPWR)
	<-h.done
}
func TestShutdownConcurrentSignals(t *testing.T) {
	g := newShutdownGate()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); g.receive(syscall.SIGPWR); g.fenceCommitted() }()
	}
	wg.Wait()
	<-g.done
}
func TestShutdownActualDisposableSignal(t *testing.T) {
	if os.Getenv("CAPACITY_SHUTDOWN_FIXTURE") == "1" {
		g, stop := watchShutdown()
		defer stop()
		fmt.Println("ready")
		r := bufio.NewReader(os.Stdin)
		if _, e := r.ReadString('\n'); e != nil {
			os.Exit(2)
		}
		// Real signal has been queued but cannot authorize an unfenced process.
		select {
		case <-g.done:
			os.Exit(3)
		default:
		}
		g.fenceCommitted()
		select {
		case <-g.done:
		case <-time.After(5 * time.Second):
			os.Exit(4)
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestShutdownActualDisposableSignal$")
	cmd.Env = append(os.Environ(), "CAPACITY_SHUTDOWN_FIXTURE=1")
	in, e := cmd.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	out, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { _ = cmd.Process.Kill() }()
	if line, e := bufio.NewReader(out).ReadString('\n'); e != nil || line != "ready\n" {
		t.Fatal("fixture readiness", line, e)
	}
	if e = cmd.Process.Signal(syscall.SIGPWR); e != nil {
		t.Fatal(e)
	}
	if _, e = in.Write([]byte("fence\n")); e != nil {
		t.Fatal(e)
	}
	_ = in.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("fixture blocked")
	}
}
