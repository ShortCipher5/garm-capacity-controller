package hostbootstrap

import (
	"context"
	"encoding/json"
	"golang.org/x/crypto/ssh"
	"io"
	"net"
	"strings"
	"testing"
)

func TestPinnedBootstrapUsesOnlyFixedCommandAndReviewedPayload(t *testing.T) {
	for _, badHost := range []bool{false, true} {
		t.Run(map[bool]string{false: "matching", true: "wrong-pin"}[badHost], func(t *testing.T) {
			r, admin := sample(t)
			host := signer(t)
			r.Plan.HostKey = string(ssh.MarshalAuthorizedKey(host.PublicKey()))
			r.ReviewDigest = Digest(r.Plan)
			l, e := net.Listen("tcp", "127.0.0.1:0")
			if e != nil {
				t.Fatal(e)
			}
			defer l.Close()
			seen := make(chan string, 1)
			go func() {
				conn, e := l.Accept()
				if e != nil {
					seen <- "accept"
					return
				}
				defer conn.Close()
				cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
					if ssh.FingerprintSHA256(k) != r.Plan.AdminKeyFingerprint {
						return nil, ErrInvalid
					}
					return nil, nil
				}}
				if badHost {
					cfg.AddHostKey(signer(t))
				} else {
					cfg.AddHostKey(host)
				}
				server, chs, req, e := ssh.NewServerConn(conn, cfg)
				if e != nil {
					seen <- "no-session"
					return
				}
				defer server.Close()
				go ssh.DiscardRequests(req)
				for incoming := range chs {
					if incoming.ChannelType() != "session" {
						incoming.Reject(ssh.UnknownChannelType, "unsupported")
						continue
					}
					ch, requests, e := incoming.Accept()
					if e != nil {
						seen <- "channel"
						return
					}
					defer ch.Close()
					for req := range requests {
						var command struct{ Command string }
						if req.Type != "exec" || ssh.Unmarshal(req.Payload, &command) != nil || command.Command != Command {
							req.Reply(false, nil)
							seen <- "unexpected command"
							return
						}
						req.Reply(true, nil)
						b, e := io.ReadAll(io.LimitReader(ch, 24<<20))
						var got Request
						if e != nil || json.Unmarshal(b, &got) != nil || Validate(got) != nil || got.ReviewDigest != r.ReviewDigest {
							seen <- "bad payload"
							return
						}
						io.WriteString(ch, "inventory-bootstrap-installed "+r.ReviewDigest+"\n")
						ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
						seen <- "fixed-command"
						return
					}
				}
			}()
			called := false
			dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
				called = true
				if addr != "192.0.2.20:22" {
					t.Error(addr)
				}
				return (&net.Dialer{}).DialContext(ctx, network, l.Addr().String())
			}
			e = execute(context.Background(), r, admin, dial)
			result := <-seen
			if !called {
				t.Fatal("dial missing")
			}
			if badHost {
				if e == nil || result != "no-session" || !strings.Contains(e.Error(), "no session") {
					t.Fatal(e, result)
				}
			} else if e != nil || result != "fixed-command" {
				t.Fatal(e, result)
			}
		})
	}
}
