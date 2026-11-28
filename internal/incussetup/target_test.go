package incussetup

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
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
	k, e := ssh.NewSignerFromKey(p)
	if e != nil {
		t.Fatal(e)
	}
	return k
}
func TestIncusSetupSSHInspect(t *testing.T) {
	for _, scenario := range []string{"success", "wrong-pin", "wrong-nonce", "fake-placement", "missing-certificate"} {
		t.Run(scenario, func(t *testing.T) {
			admin, host := signer(t), signer(t)
			target := Target{Address: "192.0.2.20", Port: 22, User: "bootstrap", HostKey: string(ssh.MarshalAuthorizedKey(host.PublicKey())), AdminFingerprint: ssh.FingerprintSHA256(admin.PublicKey())}
			local, e := net.Listen("tcp", "127.0.0.1:0")
			if e != nil {
				t.Fatal(e)
			}
			defer local.Close()
			cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
				if ssh.FingerprintSHA256(k) == target.AdminFingerprint {
					return nil, nil
				}
				return nil, Err
			}}
			if scenario == "wrong-pin" {
				cfg.AddHostKey(signer(t))
			} else {
				cfg.AddHostKey(host)
			}
			seen := make(chan bool, 1)
			go func() {
				connected := false
				defer func() { seen <- connected }()
				conn, e := local.Accept()
				if e != nil {
					return
				}
				defer conn.Close()
				server, channels, requests, e := ssh.NewServerConn(conn, cfg)
				if e != nil {
					return
				}
				defer server.Close()
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					if incoming.ChannelType() != "session" {
						return
					}
					ch, reqs, e := incoming.Accept()
					if e != nil {
						return
					}
					defer ch.Close()
					for req := range reqs {
						var p struct{ Command string }
						if req.Type != "exec" || ssh.Unmarshal(req.Payload, &p) != nil || p.Command != "/usr/bin/sudo -n /usr/bin/python3 -I -c '"+strings.ReplaceAll(recipe, "'", "'\"'\"'")+"'" {
							req.Reply(false, nil)
							return
						}
						req.Reply(true, nil)
						raw, _ := io.ReadAll(io.LimitReader(ch, 1025))
						var args []string
						if json.Unmarshal(raw, &args) != nil || len(args) != 4 || args[0] != "inspect" || args[1] != "" || args[2] != "" {
							return
						}
						connected = true
						nonce := args[3]
						if scenario == "wrong-nonce" {
							nonce = strings.Repeat("a", 64)
						}
						allow := scenario == "fake-placement"
						cert := strings.Repeat("b", 64)
						if scenario == "missing-certificate" {
							cert = ""
						}
						json.NewEncoder(ch).Encode(Report{Nonce: nonce, Outcome: "observed", State: "configured", Version: "7.0.2", Eligible: &allow, Certificate: cert})
						ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
						return
					}
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, e = target.run(ctx, admin, nil, func(ctx context.Context, network, addr string) (net.Conn, error) {
				if addr != "192.0.2.20:22" {
					t.Fatalf("unexpected destination %q", addr)
				}
				return (&net.Dialer{}).DialContext(ctx, network, local.Addr().String())
			})
			reached := <-seen
			if (e == nil) != (scenario == "success") {
				t.Fatalf("unexpected result: %v", e)
			}
			if reached == (scenario == "wrong-pin") {
				t.Fatal("unexpected authenticated command execution status")
			}
		})
	}
}
