//go:build linux

package nativeprovision

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprepare"
	"golang.org/x/crypto/ssh"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func signer(t *testing.T) ssh.Signer {
	t.Helper()
	_, p, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	s, e := ssh.NewSignerFromKey(p)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func testReceipt(m nativeprepare.Manifest) nativeprepare.Receipt {
	md, _ := nativeprepare.ManifestDigest(m)
	dest := nativeprepare.Parent + "/" + m.ID
	unit := "# PREPARED ONLY: not installed, enabled, or qualified for activation.\n[Unit]\nDescription=Prepared native owner (activation refused)\n[Service]\nType=exec\nUser=root\nGroup=root\nUMask=0077\nExecStart=" + dest + "/bin/capacity-native-domain --capsule-config=" + dest + "/private/capsule.json\nRestart=no\nProtectSystem=strict\nStandardOutput=null\nStandardError=null\n"
	return nativeprepare.Receipt{Version: nativeprepare.Version, ID: m.ID, State: "prepared_not_activated", ManifestDigest: md, UnitDigest: digest([]byte(unit))}
}
func TestPinnedSSHExactFramesAndInspectNeverUploads(t *testing.T) {
	for _, scenario := range []string{"prepare", "inspect", "wrong_pin", "wrong_receipt"} {
		t.Run(scenario, func(t *testing.T) {
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
				if request.Type != "exec" || ssh.Unmarshal(request.Payload, &exec) != nil || exec.Command != command() {
					request.Reply(false, nil)
					done <- ErrInvalid
					return
				}
				request.Reply(true, nil)
				reader := bufio.NewReader(channel)
				header, e := reader.ReadBytes('\n')
				if e != nil {
					done <- e
					return
				}
				var h struct {
					Mode     string
					Manifest nativeprepare.Manifest
					Files    []File
				}
				if json.Unmarshal(header, &h) != nil || h.Manifest.ID != p.Manifest.ID {
					done <- ErrInvalid
					return
				}
				mode := "prepare"
				if scenario == "inspect" {
					mode = "inspect"
				}
				if h.Mode != mode {
					done <- ErrInvalid
					return
				}
				body, e := io.ReadAll(reader)
				if e != nil {
					done <- e
					return
				}
				want := []byte{}
				if scenario != "inspect" {
					for _, f := range b.files {
						want = append(want, []byte(f.Name)...)
					}
				}
				if string(body) != string(want) {
					done <- ErrInvalid
					return
				}
				receipt := testReceipt(p.Manifest)
				if scenario == "wrong_receipt" {
					receipt.ActivationAuthorized = true
				}
				raw, _ := json.Marshal(receipt)
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
			receipt, e := b.execute(context.Background(), p, Digest(p), admin, scenario == "inspect", dial)
			serverErr := <-done
			if scenario == "wrong_pin" || scenario == "wrong_receipt" {
				if e == nil {
					t.Fatal("unsafe result")
				}
			} else if e != nil || serverErr != nil || receipt.ActivationAuthorized {
				t.Fatal(receipt, e, serverErr)
			}
		})
	}
}
