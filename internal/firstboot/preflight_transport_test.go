package firstboot

import (
	"context"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativepreflight"
	"golang.org/x/crypto/ssh"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestPinnedAdminFixedDiagnosticAndReport(t *testing.T) {
	for _, scenario := range []string{"success", "wrong-pin", "wrong-nonce", "qualified"} {
		t.Run(scenario, func(t *testing.T) {
			r, admin := fixture(t)
			host := key(t)
			r.Plan.Base.HostKey = string(ssh.MarshalAuthorizedKey(host.PublicKey()))
			r.ReviewDigest = Digest(r.Plan)
			l, e := net.Listen("tcp", "127.0.0.1:0")
			if e != nil {
				t.Fatal(e)
			}
			defer l.Close()
			done := make(chan string, 1)
			go func() {
				conn, e := l.Accept()
				if e != nil {
					done <- "accept"
					return
				}
				defer conn.Close()
				config := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
					if ssh.FingerprintSHA256(k) != r.Plan.Base.AdminKeyFingerprint {
						return nil, ErrInvalid
					}
					return nil, nil
				}}
				if scenario == "wrong-pin" {
					config.AddHostKey(key(t))
				} else {
					config.AddHostKey(host)
				}
				server, channels, requests, e := ssh.NewServerConn(conn, config)
				if e != nil {
					done <- "no-session"
					return
				}
				defer server.Close()
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					if incoming.ChannelType() != "session" {
						incoming.Reject(ssh.UnknownChannelType, "")
						continue
					}
					channel, requests, e := incoming.Accept()
					if e != nil {
						done <- "channel"
						return
					}
					defer channel.Close()
					for request := range requests {
						var payload struct{ Command string }
						if request.Type != "exec" || ssh.Unmarshal(request.Payload, &payload) != nil || payload.Command != preflightCommand() {
							request.Reply(false, nil)
							done <- "wrong-command"
							return
						}
						request.Reply(true, nil)
						raw, e := io.ReadAll(io.LimitReader(channel, 24<<20))
						var got map[string]string
						if e != nil || json.Unmarshal(raw, &got) != nil || len(got) != 2 || got["sha256"] != strings.Repeat("a", 64) || !preflightHash(got["nonce"]) {
							done <- "bad-payload"
							return
						}
						report := nativepreflight.Report{Version: "native-host-preflight/v1", State: "unqualified_no_activation", ObservedAt: time.Now(), Checks: []nativepreflight.Check{{Name: "arc", State: "unqualified", Required: "hard bound unavailable"}}}
						nonce := got["nonce"]
						if scenario == "wrong-nonce" {
							nonce = strings.Repeat("b", 64)
						}
						if scenario == "qualified" {
							report.State = "ready"
						}
						data, _ := json.Marshal(map[string]any{"nonce": nonce, "report": report})
						channel.Write(data)
						channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
						done <- "fixed-command"
						return
					}
				}
			}()
			dial := func(ctx context.Context, network, address string) (net.Conn, error) {
				if address != "192.0.2.20:22" {
					t.Error(address)
				}
				return (&net.Dialer{}).DialContext(ctx, network, l.Addr().String())
			}
			receipt, e := executePreflight(context.Background(), DeploymentTarget{Request: r, CredentialAlias: "admin", PreflightSHA256: strings.Repeat("a", 64)}, admin, dial)
			result := <-done
			if scenario == "success" {
				if e != nil || receipt.State != "unqualified_no_activation" || result != "fixed-command" {
					t.Fatal(e, receipt, result)
				}
			} else if e == nil {
				t.Fatal("unsafe receipt/pin accepted")
			}
			if scenario == "wrong-pin" && result != "no-session" {
				t.Fatal(result)
			}
		})
	}
}
