package remotedisktelemetry

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func startupFixture(t *testing.T) (time.Time, localReviewDocument, []byte) {
	t.Helper()
	at, review, _, _, _ := fixture(t)
	server, client := [32]byte{3}, [32]byte{4}
	doc := localReviewDocument{
		Kind: "reviewed-local-runner-disk/v1", Scope: projectScope(review.Scope),
		DirectoryInode: review.DirectoryInode, ExpiresAt: at.Add(time.Minute).Format(time.RFC3339Nano),
		ServerKey: hex.EncodeToString(server[:]), ClientKey: hex.EncodeToString(client[:]),
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return at, doc, raw
}

func TestLocalDiskStartupReviewExactScopeAndAbsoluteLease(t *testing.T) {
	at, doc, raw := startupFixture(t)
	got, ok := parseLocalReview(append(bytes.Clone(raw), '\n'), at)
	if !ok || got.inode != doc.DirectoryInode || projectScope(got.scope) != doc.Scope || got.until.Format(time.RFC3339Nano) != doc.ExpiresAt {
		t.Fatal("valid fixture was refused")
	}
	for _, payload := range [][]byte{
		append(bytes.Clone(raw), '\n', '\n'),
		bytes.Repeat([]byte("x"), localMaxReviewFile+1),
		[]byte(strings.Replace(string(raw), `"directory_inode":123`, `"directory_inode":123,"directory_inode":123`, 1)),
		[]byte(strings.Replace(string(raw), `"directory_inode":123`, `"directory_inode":0`, 1)),
		[]byte(strings.Replace(string(raw), `"scope":`, `"scope" :`, 1)),
		[]byte(strings.Replace(string(raw), `"scope":`, `"filesystem_path":"/tmp","scope":`, 1)),
		[]byte(strings.Replace(string(raw), doc.ClientKey, doc.ServerKey, 1)),
		[]byte(strings.Replace(string(raw), doc.ExpiresAt, at.Format(time.RFC3339Nano), 1)),
		[]byte(strings.Replace(string(raw), doc.ExpiresAt, at.Add(localMaxLease+time.Nanosecond).Format(time.RFC3339Nano), 1)),
	} {
		if bytes.Equal(raw, payload) {
			t.Fatal("broken negative fixture")
		}
		if got, ok := parseLocalReview(payload, at); ok || got != (localReview{}) {
			t.Fatal("unreviewed startup data accepted")
		}
	}
}

func TestLocalDiskStartupDoesNotBindWithoutOptInAndExactHeldDescriptor(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"serve-reviewed", "/tmp"}, {"serve-reviewed=false"}} {
		if err := ServeLocalInherited(t.Context(), args); err != ErrLocalStartup {
			t.Fatal("unsupported/default launch succeeded", err)
		}
	}
	upstream, client, binding, _ := testHTTPRoute(t, http.NotFoundHandler())
	trust := &tls.Config{RootCAs: client.TLS.RootCAs, Certificates: upstream.TLS.Certificates}
	review := localReview{scope: binding.Scope, inode: 1, until: binding.ExpiresAt, serverKey: binding.PeerKeySHA256, clientKey: client.ClientKeySHA256}
	dir, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	// This temporary directory is not an independently matching bind; the
	// strict sampler must not create even a fixture listener from it.
	if err := localServeLease(ctx, dir, review, trust, func(context.Context, string, string) (net.Listener, error) {
		t.Error("startup reached listen without the independently reviewed actual descriptor")
		return nil, errors.New("unexpected fixture listen")
	}); err != ErrLocalStartup {
		t.Fatal(err)
	}
	for end := time.Now().Add(time.Second); ; {
		_, err := dir.Stat()
		if errors.Is(err, os.ErrClosed) {
			break
		}
		if time.Now().After(end) {
			t.Fatal("rejected startup retained its owned FD")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestLocalDiskStartupTLSRoleAndHTTPBounds(t *testing.T) {
	s, client, binding, _ := testHTTPRoute(t, http.NotFoundHandler())
	input := &tls.Config{RootCAs: client.TLS.RootCAs, Certificates: s.TLS.Certificates}
	review := localReview{scope: binding.Scope, inode: binding.DirectoryInode, until: binding.ExpiresAt, serverKey: binding.PeerKeySHA256, clientKey: client.ClientKeySHA256}
	config, err := localSenderTLS(input, review, time.Now().UTC())
	if err != nil || config.ClientAuth != tls.RequireAndVerifyClientCert || config.ClientCAs == nil ||
		config.MinVersion != tls.VersionTLS13 || config.MaxVersion != tls.VersionTLS13 || !config.SessionTicketsDisabled ||
		len(config.NextProtos) != 1 || config.NextProtos[0] != "http/1.1" {
		t.Fatal("standalone TLS role enforcement not configured", err)
	}
	if _, err := localSenderTLS(client.TLS, review, time.Now().UTC()); err != ErrLocalStartup {
		t.Fatal("wrong role accepted as server", err)
	}
	other := review
	other.serverKey[0] ^= 1
	if _, err := localSenderTLS(input, other, time.Now().UTC()); err != ErrLocalStartup {
		t.Fatal("wrong server SPKI accepted", err)
	}
	expired := review
	expired.until = s.Certificate().NotAfter.Add(time.Second)
	if _, err := localSenderTLS(input, expired, time.Now().UTC()); err != ErrLocalStartup {
		t.Fatal("startup could outlive server certificate", err)
	}
	server := localSenderServer(nil, config, t.Context())
	if server.Protocols == nil || !server.Protocols.HTTP1() || server.Protocols.HTTP2() ||
		server.ReadHeaderTimeout != time.Second || server.ReadTimeout != 3*time.Second ||
		server.WriteTimeout != 3*time.Second || server.MaxHeaderBytes != 4<<10 ||
		localListenAddr != "127.0.0.1:9447" || localMaxConnections != 4 {
		t.Fatal("standalone network bounds drifted")
	}
}

type fixtureLocalListener struct{ conn net.Conn }

func (l fixtureLocalListener) Accept() (net.Conn, error) { return l.conn, nil }
func (l fixtureLocalListener) Close() error              { return nil }
func (l fixtureLocalListener) Addr() net.Addr            { return l.conn.LocalAddr() }

func TestLocalLimitedListenerKeepsSlotUntilConnectionClose(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	l := &localLimitListener{Listener: fixtureLocalListener{a}, slots: make(chan struct{}, 1), done: make(chan struct{})}
	first, err := l.Accept()
	if err != nil || len(l.slots) != 1 {
		t.Fatal("did not acquire socket slot", err)
	}
	exit := make(chan error, 1)
	go func() {
		_, err := l.Accept()
		exit <- err
	}()
	select {
	case err := <-exit:
		t.Fatal("second connection escaped connection bound", err)
	case <-time.After(10 * time.Millisecond):
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exit:
		if !errors.Is(err, net.ErrClosed) || len(l.slots) != 1 {
			t.Fatal("listener close freed an active slot or failed to stop accept", err)
		}
	case <-time.After(time.Second):
		t.Fatal("closed listener did not unblock accept")
	}
	_ = first.Close()
	_ = first.Close()
	if len(l.slots) != 0 {
		t.Fatal("closing the owned connection did not return slot once")
	}
}

func TestLocalDiskStartupActualTLSRequiresAuthorizedClient(t *testing.T) {
	source, client, binding, _ := testHTTPRoute(t, http.NotFoundHandler())
	trustedFiles := &tls.Config{RootCAs: client.TLS.RootCAs, Certificates: source.TLS.Certificates}
	review := localReview{scope: binding.Scope, inode: binding.DirectoryInode, until: binding.ExpiresAt, serverKey: binding.PeerKeySHA256, clientKey: client.ClientKeySHA256}
	trustedServer, err := localSenderTLS(trustedFiles, review, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}

	// Exercise the actual server TLS settings on a disposable loopback socket
	// only; never start the installed command or invent a good mount proof.
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	server := localSenderServer(nil, trustedServer, t.Context())
	exited := make(chan error, 1)
	go func() { exited <- server.ServeTLS(ln, "", "") }()
	t.Cleanup(func() {
		_ = server.Close()
		select {
		case <-exited:
		case <-time.After(time.Second):
			t.Error("fixture listener did not exit")
		}
	})
	endpoint := "https://" + ln.Addr().String() + HTTPPath
	raw, _ := encodeHTTPChallenge([32]byte{2})
	if status, _ := sendFixtureRequest(t, client.TLS, endpoint, http.MethodPost, "application/json", raw); status != http.StatusServiceUnavailable {
		t.Fatal("missing valid sampler unexpectedly produced a reading", status)
	}
	noRole := client.TLS.Clone()
	noRole.Certificates = nil
	transport := &http.Transport{TLSClientConfig: noRole, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if res, err := transport.RoundTrip(req); err == nil {
		res.Body.Close()
		t.Fatal("server accepted TLS without a client certificate")
	}
}
