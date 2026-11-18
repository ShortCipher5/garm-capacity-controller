package sshinventory

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/hostinventory"
	"golang.org/x/crypto/ssh"
)

const sample = "garm_inventory_v1\ncpu_online=8\nram_kib=16777216\navailable_ram_kib=4194304\nroot_disk_kib=104857600\nroot_available_kib=52428800\n"

func signer(t *testing.T) ssh.Signer {
	t.Helper()
	_, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	s, e := ssh.NewSignerFromKey(key)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func server(t *testing.T, output string, forced ...bool) (Config, KeyResolver, *atomic.Int32) {
	t.Helper()
	host, user := signer(t), signer(t)
	auths := &atomic.Int32{}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { listener.Close() })
	config := &ssh.ServerConfig{PublicKeyCallback: func(metadata ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		auths.Add(1)
		if metadata.User() != "inventory" || string(key.Marshal()) != string(user.PublicKey().Marshal()) {
			return nil, ErrInventory
		}
		return nil, nil
	}}
	config.AddHostKey(host)
	go func() {
		raw, e := listener.Accept()
		if e != nil {
			return
		}
		defer raw.Close()
		conn, channels, requests, e := ssh.NewServerConn(raw, config)
		if e != nil {
			return
		}
		defer conn.Close()
		go ssh.DiscardRequests(requests)
		for channel := range channels {
			if channel.ChannelType() != "session" {
				channel.Reject(ssh.UnknownChannelType, "unsupported")
				continue
			}
			stream, requests, e := channel.Accept()
			if e != nil {
				return
			}
			for req := range requests {
				if req.Type != "exec" {
					req.Reply(false, nil)
					continue
				}
				var command struct{ Command string }
				if ssh.Unmarshal(req.Payload, &command) != nil || command.Command != "/bin/sh -s" {
					req.Reply(false, nil)
					return
				}
				req.Reply(true, nil)
				script, e := io.ReadAll(io.LimitReader(stream, 8193))
				if e != nil {
					return
				}
				if len(forced) == 0 && string(script) != probeScript {
					t.Error("unreviewed command script")
				}
				if len(forced) > 0 {
					if len(script) > 8192 {
						t.Error("oversized input")
					}
					hostinventory.Write(stream, hostinventory.Collect())
				} else {
					io.WriteString(stream, output)
				}
				stream.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
				stream.Close()
				return
			}
		}
	}()
	address := listener.Addr().(*net.TCPAddr)
	return Config{Address: "127.0.0.1", Port: uint16(address.Port), User: "inventory", HostKey: string(ssh.MarshalAuthorizedKey(host.PublicKey())), CredentialRef: "existing-key"}, func(ctx context.Context, ref string) (ssh.Signer, error) {
		if ref != "existing-key" {
			return nil, ErrInventory
		}
		return user, nil
	}, auths
}
func TestPinnedReadOnlyProbe(t *testing.T) {
	config, resolve, _ := server(t, sample)
	c, e := New(config, resolve)
	if e != nil {
		t.Fatal(e)
	}
	result, e := c.Probe(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if !result.Complete || result.PhysicalCapacityQualified || *result.LogicalCPUs != 8 || *result.RAMMiB != 16384 || *result.AvailableRAMMiB != 4096 || *result.RootDiskMiB != 102400 || *result.RootAvailableMiB != 51200 || result.ObservedAt.IsZero() {
		t.Fatalf("%+v", result)
	}
}
func TestWrongHostKeyStopsBeforeAuthentication(t *testing.T) {
	config, resolve, auths := server(t, sample)
	config.HostKey = string(ssh.MarshalAuthorizedKey(signer(t).PublicKey()))
	c, e := New(config, resolve)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.Probe(context.Background()); e != ErrInventory || auths.Load() != 0 {
		t.Fatalf("host mismatch allowed auth %v/%d", e, auths.Load())
	}
}
func TestMalformedAndExcessOutputRejected(t *testing.T) {
	for _, output := range []string{"private-message", strings.Repeat("x", 8192), strings.Replace(sample, "ram_kib=16777216", "ram_kib=-1", 1)} {
		cconfig, resolve, _ := server(t, output)
		c, _ := New(cconfig, resolve)
		if _, e := c.Probe(context.Background()); e != ErrInventory {
			t.Fatal("invalid output accepted")
		}
	}
}
func TestHandshakeCancellation(t *testing.T) {
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	done := make(chan struct{})
	go func() {
		conn, e := l.Accept()
		if e == nil {
			defer conn.Close()
			io.Copy(io.Discard, conn)
		}
		close(done)
	}()
	host, user := signer(t), signer(t)
	c, e := New(Config{Address: "127.0.0.1", Port: uint16(l.Addr().(*net.TCPAddr).Port), User: "inventory", HostKey: string(ssh.MarshalAuthorizedKey(host.PublicKey())), CredentialRef: "key"}, func(context.Context, string) (ssh.Signer, error) { return user, nil })
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, e = c.Probe(ctx); e != ErrInventory || time.Since(start) > time.Second {
		t.Fatal("cancellation not bounded")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("connection leaked")
	}
}
func TestConfigAndUnknownParsing(t *testing.T) {
	config := Config{Address: "127.0.0.1", Port: 22, User: "inventory", HostKey: string(ssh.MarshalAuthorizedKey(signer(t).PublicKey())), CredentialRef: "key"}
	resolve := func(context.Context, string) (ssh.Signer, error) { return nil, ErrInventory }
	for _, change := range []func(*Config){func(c *Config) { c.Address = "example.com" }, func(c *Config) { c.User = "root; echo bad" }, func(c *Config) { c.HostKey += "\n" + c.HostKey }, func(c *Config) { c.HostKey = "command=evil " + c.HostKey }, func(c *Config) { c.Port = 0 }} {
		copy := config
		change(&copy)
		if _, e := New(copy, resolve); e != ErrInventory {
			t.Fatal("invalid config accepted")
		}
	}
	result, e := parse([]byte(strings.ReplaceAll(sample, "available_ram_kib=4194304", "available_ram_kib=unknown")))
	if e != nil || result.Complete || result.AvailableRAMMiB != nil || result.RAMMiB == nil {
		t.Fatal("unknown became zero or complete")
	}
}
func TestPrivateKeyAliasProtection(t *testing.T) {
	_, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	der, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "key")
	os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0600)
	files := map[string]string{"configured": path}
	resolve := PrivateKeyFiles(files)
	files["configured"] = "different"
	if _, e := resolve(context.Background(), "configured"); e != nil {
		t.Fatal(e)
	}
	if _, e := resolve(context.Background(), path); e != ErrInventory {
		t.Fatal("arbitrary path accepted")
	}
	os.Chmod(path, 0644)
	if _, e := resolve(context.Background(), "configured"); e != ErrInventory {
		t.Fatal("public key file accepted")
	}
}

func TestParserRejectsInconsistentOrAmbiguousDimensions(t *testing.T) {
	for _, input := range []string{
		strings.Replace(sample, "cpu_online=8", "cpu_online=0", 1),
		strings.Replace(sample, "cpu_online=8", "cpu_online=8.5", 1),
		strings.Replace(sample, "ram_kib=16777216", "ram_kib=1", 1),
		strings.Replace(sample, "available_ram_kib=4194304", "available_ram_kib=999999999", 1),
		strings.Replace(sample, "root_available_kib=52428800", "root_available_kib=999999999", 1),
		strings.Replace(sample, "available_ram_kib=4194304", "ram_kib=4194304", 1),
		sample + "unexpected=value\n",
	} {
		if _, e := parse([]byte(input)); e != ErrInventory {
			t.Fatal("accepted malformed dimensions")
		}
	}
	input := strings.Replace(sample, "available_ram_kib=4194304", "available_ram_kib=0", 1)
	result, e := parse([]byte(input))
	if e != nil || result.AvailableRAMMiB == nil || *result.AvailableRAMMiB != 0 {
		t.Fatal("known zero became unknown")
	}
}

func TestForcedCommandProbe(t *testing.T) {
	for i := 0; i < 20; i++ {
		config, resolve, _ := server(t, "", true)
		client, e := New(config, resolve)
		if e != nil {
			t.Fatal(e)
		}
		result, e := client.Probe(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		if result.Source != "authenticated_linux_os_view" || result.PhysicalCapacityQualified {
			t.Fatal(result)
		}
	}
}
