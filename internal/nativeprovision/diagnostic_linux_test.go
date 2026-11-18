//go:build linux

package nativeprovision

import (
	"context"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativepreflight"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprepare"
	"golang.org/x/crypto/ssh"
	"io"
	"net"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPreparedDiagnosticPinnedSSH(t *testing.T) {
	for _, scenario := range []string{"success", "wrong_pin", "wrong_nonce", "qualified", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			b, target := fixture(t)
			admin, host := signer(t), signer(t)
			target.AdminKeyFingerprint = ssh.FingerprintSHA256(admin.PublicKey())
			target.HostKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(host.PublicKey())))
			p, e := b.Plan(target)
			if e != nil {
				t.Fatal(e)
			}
			listener, e := net.Listen("tcp", "127.0.0.1:0")
			if e != nil {
				t.Fatal(e)
			}
			defer listener.Close()
			done := make(chan error, 1)
			go func() {
				conn, e := listener.Accept()
				if e != nil {
					done <- e
					return
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
					if ssh.FingerprintSHA256(k) != target.AdminKeyFingerprint {
						return nil, ErrInvalid
					}
					return nil, nil
				}}
				if scenario == "wrong_pin" {
					cfg.AddHostKey(signer(t))
				} else {
					cfg.AddHostKey(host)
				}
				server, channels, requests, e := ssh.NewServerConn(conn, cfg)
				if e != nil {
					done <- e
					return
				}
				defer server.Close()
				go ssh.DiscardRequests(requests)
				incoming := <-channels
				channel, reqs, e := incoming.Accept()
				if e != nil {
					done <- e
					return
				}
				defer channel.Close()
				request := <-reqs
				var exec struct{ Command string }
				if request.Type != "exec" || ssh.Unmarshal(request.Payload, &exec) != nil || exec.Command != diagnosticCommand() {
					request.Reply(false, nil)
					done <- ErrInvalid
					return
				}
				request.Reply(true, nil)
				rawRequest, e := io.ReadAll(channel)
				if e != nil {
					done <- e
					return
				}
				var requestBody struct {
					SHA256  string                `json:"sha256"`
					Nonce   string                `json:"nonce"`
					Receipt nativeprepare.Receipt `json:"receipt"`
				}
				if json.Unmarshal(rawRequest, &requestBody) != nil || requestBody.SHA256 != p.Manifest.Files["capacity-native-preflight"] || requestBody.Receipt != testReceipt(p.Manifest) {
					done <- ErrInvalid
					return
				}
				if scenario == "cancel" {
					cancel()
					e := server.Wait()
					if e == nil {
						done <- ErrInvalid
					} else {
						done <- nil
					}
					return
				}
				report := nativepreflight.Report{Version: "native-host-preflight/v1", State: "unqualified_no_activation", ObservedAt: time.Now(), Checks: []nativepreflight.Check{{Name: "test", State: "unqualified"}}}
				if scenario == "qualified" {
					report.State = "ready"
				}
				if scenario == "wrong_nonce" {
					requestBody.Nonce = strings.Repeat("0", 64)
				}
				raw, _ := json.Marshal(map[string]any{"nonce": requestBody.Nonce, "report": report})
				channel.Write(raw)
				channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
				done <- nil
			}()
			dial := func(ctx context.Context, network, address string) (net.Conn, error) {
				if address != "192.0.2.1:22" {
					t.Error(address)
				}
				return (&net.Dialer{}).DialContext(ctx, network, listener.Addr().String())
			}
			report, e := executeDiagnostic(ctx, p, testReceipt(p.Manifest), admin, dial)
			serverErr := <-done
			if scenario == "wrong_pin" || scenario == "wrong_nonce" || scenario == "qualified" || scenario == "cancel" {
				if e == nil {
					t.Fatal("unsafe result")
				}
			} else if e != nil || serverErr != nil || report.State != "unqualified_no_activation" {
				t.Fatal(report, e, serverErr)
			}
		})
	}
}

func TestPreparedDiagnosticRequiresCompletionAndKeepsHistory(t *testing.T) {
	s := attemptFixture(t)
	s.life, s.cancel = context.WithCancel(context.Background())
	defer s.cancel()
	calls := 0
	resolve := func() (ssh.Signer, error) { calls++; return nil, ErrInvalid }
	recipe := s.diagnosticRecipe()
	if _, e := s.Diagnostic(context.Background(), s.plan.Manifest.ID, recipe.ReviewDigest, resolve); e == nil || calls != 0 {
		t.Fatal("unprepared diagnostic")
	}
	if _, e := s.Apply(context.Background(), s.ReviewDigest(), false, func() (ssh.Signer, error) { return nil, nil }); e != nil {
		t.Fatal(e)
	}
	before, e := s.Status()
	if e != nil {
		t.Fatal(e)
	}
	entries, _ := os.ReadDir(s.dir)
	if _, e = s.Diagnostic(context.Background(), strings.Repeat("0", 64), recipe.ReviewDigest, resolve); e == nil || calls != 0 {
		t.Fatal("stale association")
	}
	if _, e = s.Diagnostic(context.Background(), s.plan.Manifest.ID, recipe.ReviewDigest, resolve); e == nil || calls != 1 {
		t.Fatal("resolver gate", e, calls)
	}
	after, e := s.Status()
	later, _ := os.ReadDir(s.dir)
	if e != nil || !reflect.DeepEqual(before, after) || len(entries) != len(later) {
		t.Fatal("diagnostic mutated history")
	}
	s.busy.Store(true)
	if _, e = s.Diagnostic(context.Background(), s.plan.Manifest.ID, recipe.ReviewDigest, resolve); e == nil || calls != 1 {
		t.Fatal("concurrent diagnostic")
	}
	s.busy.Store(false)
	s.closed.Store(true)
	if _, e = s.Diagnostic(context.Background(), s.plan.Manifest.ID, recipe.ReviewDigest, resolve); e == nil || calls != 1 {
		t.Fatal("closed diagnostic")
	}
}
func TestPreparedDiagnosticReviewBindsCurrentPlan(t *testing.T) {
	s := attemptFixture(t)
	a := s.diagnosticRecipe()
	s.plan.Target.Port++
	if a.ReviewDigest == s.diagnosticRecipe().ReviewDigest {
		t.Fatal("target not bound")
	}
}

func TestPreparedDiagnosticCloseCancelsActualSSHAndJoins(t *testing.T) {
	s := attemptFixture(t)
	s.life, s.cancel = context.WithCancel(context.Background())
	admin, host := signer(t), signer(t)
	s.plan.Target.AdminKeyFingerprint = ssh.FingerprintSHA256(admin.PublicKey())
	s.plan.Target.HostKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(host.PublicKey())))
	if _, e := s.Apply(context.Background(), s.ReviewDigest(), false, func() (ssh.Signer, error) { return nil, nil }); e != nil {
		t.Fatal(e)
	}
	before, e := s.Status()
	if e != nil {
		t.Fatal(e)
	}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	entered := make(chan struct{})
	serverDone := make(chan error, 1)
	go func() {
		conn, e := listener.Accept()
		if e != nil {
			serverDone <- e
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(30 * time.Second))
		config := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if ssh.FingerprintSHA256(key) != s.plan.Target.AdminKeyFingerprint {
				return nil, ErrInvalid
			}
			return nil, nil
		}}
		config.AddHostKey(host)
		server, channels, requests, e := ssh.NewServerConn(conn, config)
		if e != nil {
			serverDone <- e
			return
		}
		defer server.Close()
		go ssh.DiscardRequests(requests)
		incoming, ok := <-channels
		if !ok {
			serverDone <- ErrInvalid
			return
		}
		channel, reqs, e := incoming.Accept()
		if e != nil {
			serverDone <- e
			return
		}
		defer channel.Close()
		request := <-reqs
		request.Reply(true, nil)
		if _, e = io.ReadAll(channel); e != nil {
			serverDone <- e
			return
		}
		close(entered)
		// Keep the actual session pending until client shutdown closes the connection.
		e = server.Wait()
		if e == nil {
			serverDone <- ErrInvalid
		} else {
			serverDone <- nil
		}
	}()
	done := make(chan error, 1)
	go func() {
		_, e := s.diagnostic(context.Background(), s.plan.Manifest.ID, s.diagnosticRecipe().ReviewDigest, func() (ssh.Signer, error) { return admin, nil }, func(ctx context.Context, network, address string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, listener.Addr().String())
		})
		done <- e
	}()
	select {
	case <-entered:
	case e := <-serverDone:
		t.Fatal(e)
	case <-time.After(5 * time.Second):
		t.Fatal("request not entered")
	}
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("close did not join")
	}
	if e := <-done; e == nil {
		t.Fatal("canceled diagnostic succeeded")
	}
	if e := <-serverDone; e != nil {
		t.Fatal(e)
	}
	after, e := s.Status()
	if e != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("shutdown changed preparation history", e)
	}
}
