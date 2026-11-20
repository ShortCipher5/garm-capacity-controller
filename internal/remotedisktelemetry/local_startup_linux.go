package remotedisktelemetry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/hostinventory"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	"golang.org/x/sys/unix"
)

const (
	localReviewRoot     = "/etc/capacity-runner-disk-telemetry"
	localListenAddr     = "127.0.0.1:9447"
	localDirectoryFD    = 3
	localMaxLease       = 30 * time.Minute
	localMaxConnections = 4
	localMaxReviewFile  = 2048
)

var ErrLocalStartup = errors.New("reviewed local runner disk telemetry unavailable")
var ErrLocalDaemonCustody = errors.New("independently reviewed daemon mount custody unavailable")

type localReviewDocument struct {
	Kind           string    `json:"kind"`
	Scope          wireScope `json:"scope"`
	DirectoryInode uint64    `json:"directory_inode"`
	ExpiresAt      string    `json:"expires_at"`
	ServerKey      string    `json:"server_key_sha256"`
	ClientKey      string    `json:"client_key_sha256"`
}

type localReview struct {
	scope                hostinventory.DirFilesystemScope
	inode                uint64
	until                time.Time
	serverKey, clientKey [32]byte
}

func parseLocalReview(raw []byte, now time.Time) (localReview, bool) {
	var none localReview
	if len(raw) == 0 || len(raw) > localMaxReviewFile {
		return none, false
	}
	raw = bytes.TrimSuffix(raw, []byte("\n"))
	var doc localReviewDocument
	if len(raw) == 0 || json.Unmarshal(raw, &doc) != nil ||
		doc.Kind != "reviewed-local-runner-disk/v1" {
		return none, false
	}
	canonical, err := json.Marshal(doc)
	if err != nil || !bytes.Equal(raw, canonical) {
		return none, false
	}
	scope := hostinventory.DirFilesystemScope{
		HostFingerprint: doc.Scope.Host, StoragePool: doc.Scope.Pool, BootID: doc.Scope.Boot,
		MountNamespace: doc.Scope.MountNamespace, MountID: doc.Scope.UniqueMountID,
		DeviceMajor: doc.Scope.DeviceMajor, DeviceMinor: doc.Scope.DeviceMinor,
	}
	expiry, err := time.Parse(time.RFC3339Nano, doc.ExpiresAt)
	now = now.UTC()
	if err != nil || !wireTime(expiry) || expiry.UTC().Format(time.RFC3339Nano) != doc.ExpiresAt ||
		!wireTime(now) || !now.Before(expiry) || expiry.After(now.Add(localMaxLease)) || !scope.Valid() || doc.DirectoryInode == 0 {
		return none, false
	}
	review := localReview{scope: scope, inode: doc.DirectoryInode, until: expiry.UTC()}
	for _, key := range []struct {
		hex string
		out *[32]byte
	}{{doc.ServerKey, &review.serverKey}, {doc.ClientKey, &review.clientKey}} {
		if n, err := hex.Decode(key.out[:], []byte(key.hex)); err != nil || n != len(key.out) ||
			key.hex != hex.EncodeToString(key.out[:]) || *key.out == ([32]byte{}) {
			return none, false
		}
	}
	if review.clientKey == review.serverKey {
		return none, false
	}
	return review, true
}

func localSenderTLS(source *tls.Config, review localReview, now time.Time) (*tls.Config, error) {
	if source == nil || source.RootCAs == nil || len(source.Certificates) != 1 || review.serverKey == ([32]byte{}) ||
		review.clientKey == ([32]byte{}) || review.clientKey == review.serverKey || !now.Before(review.until) ||
		len(source.Certificates[0].Certificate) == 0 {
		return nil, ErrLocalStartup
	}
	cert := source.Certificates[0]
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil || cert.PrivateKey == nil || leaf.IsCA || leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 ||
		len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth || len(leaf.UnknownExtKeyUsage) != 0 ||
		!httpRoleTime(leaf, now) || review.until.After(leaf.NotAfter) || sha256.Sum256(leaf.RawSubjectPublicKeyInfo) != review.serverKey {
		return nil, ErrLocalStartup
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13,
		ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: source.RootCAs.Clone(),
		Certificates: []tls.Certificate{cert}, SessionTicketsDisabled: true, NextProtos: []string{"http/1.1"},
	}, nil
}

func localSenderServer(handler *HTTPSender, trust *tls.Config, lease context.Context) *http.Server {
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	s := &http.Server{
		Handler: handler, TLSConfig: trust, Protocols: protocols,
		BaseContext:       func(net.Listener) context.Context { return lease },
		ReadHeaderTimeout: time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second,
		IdleTimeout: time.Second, MaxHeaderBytes: 4 << 10,
		ErrorLog: log.New(io.Discard, "", 0),
	}
	s.SetKeepAlivesEnabled(false)
	return s
}

// localLimitListener bounds even sockets still in the TLS handshake or header
// phase. A slot is returned only when the corresponding socket closes.
type localLimitListener struct {
	net.Listener
	slots chan struct{}
	done  chan struct{}
	once  sync.Once
}

type localLimitedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (l *localLimitListener) Accept() (net.Conn, error) {
	select {
	case <-l.done:
		return nil, net.ErrClosed
	case l.slots <- struct{}{}:
	}
	conn, err := l.Listener.Accept()
	if err != nil {
		<-l.slots
		return nil, err
	}
	return &localLimitedConn{Conn: conn, release: func() { <-l.slots }}, nil
}

func (l *localLimitListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return l.Listener.Close()
}

func (c *localLimitedConn) Close() error {
	defer c.once.Do(c.release)
	return c.Conn.Close()
}

func exactPreflight(ctx context.Context, sampler *hostinventory.DirFilesystemCollector, review localReview) bool {
	if sampler == nil || ctx.Err() != nil {
		return false
	}
	limit, cancel := context.WithTimeout(ctx, senderBudget)
	defer cancel()
	reply := make(chan bool, 1)
	go func() {
		observation, ok := sampler.Collect(limit)
		sample, inode, expiry, proven := observation.Evidence()
		reply <- ok && proven && sample.Scope == review.scope && inode == review.inode && expiry.UTC().Equal(review.until)
	}()
	select {
	case <-limit.Done():
		return false
	case ready := <-reply:
		return ready && limit.Err() == nil && time.Now().UTC().Before(review.until)
	}
}

// localServeLease is intentionally not reachable from the installed command.
// Current process visibility alone cannot prove the live Incus route. A future
// reviewed production custody source must guard the call; no caller boolean or
// owner-file assertion can stand in for that authority.
func localServeLease(ctx context.Context, dir *os.File, review localReview, loadedTLS *tls.Config,
	listen func(context.Context, string, string) (net.Listener, error)) error {
	if dir == nil {
		return ErrLocalStartup
	}
	transferred := false
	defer func() {
		if !transferred {
			_ = dir.Close()
		}
	}()
	if ctx == nil || ctx.Err() != nil || listen == nil {
		return ErrLocalStartup
	}
	lease, cancel := context.WithDeadline(ctx, review.until)
	defer cancel()
	if !currentLocalDirectoryMount(lease, dir, review) {
		return ErrLocalStartup
	}
	trust, err := localSenderTLS(loadedTLS, review, time.Now().UTC())
	if err != nil {
		return ErrLocalStartup
	}
	scope := review.scope
	sampler, err := hostinventory.NewDirFilesystemCollector(lease, hostinventory.DirFilesystemCollectorInput{
		Enabled: true, Directory: dir, Scope: scope, Binding: hostinventory.DirFilesystemFDBinding{
			HostFingerprint: scope.HostFingerprint, StoragePool: scope.StoragePool, BootID: scope.BootID,
			MountNamespace: scope.MountNamespace, FilesystemType: "ext4", DirectoryInode: review.inode, ReviewExpiresAt: review.until,
		},
	})
	if err != nil {
		return ErrLocalStartup
	}
	transferred = true // the existing collector closes on lease cancellation
	if !exactPreflight(lease, sampler, review) {
		return ErrLocalStartup
	}
	sender, err := NewHTTPSender(lease, HTTPSenderInput{
		ClientKeySHA256: review.clientKey, Scope: scope, DirectoryInode: review.inode,
		ReviewExpiresAt: review.until, Collect: sampler.Collect,
	})
	if err != nil || !currentLocalDirectoryMount(lease, dir, review) {
		return ErrLocalStartup
	}
	raw, err := listen(lease, "tcp4", localListenAddr)
	if err != nil || raw == nil {
		return ErrLocalStartup
	}
	l := &localLimitListener{Listener: raw, slots: make(chan struct{}, localMaxConnections), done: make(chan struct{})}
	defer l.Close()
	server := localSenderServer(sender, trust, lease)
	defer server.Close()
	stop := context.AfterFunc(lease, func() { _ = server.Close(); _ = l.Close() })
	defer stop()
	if !localLeaseCurrent(lease) {
		return ErrLocalStartup
	}
	if err := server.ServeTLS(l, "", ""); lease.Err() == nil || (!errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed)) {
		return ErrLocalStartup
	}
	return nil
}

func localLeaseCurrent(ctx context.Context) bool {
	end, ok := ctx.Deadline()
	return ok && ctx.Err() == nil && time.Now().UTC().Before(end.UTC())
}

// ServeLocalInherited is the opt-in preflight-only command entrypoint. A
// startup owner passes its already-open directory on FD 3. There is no path
// fallback or namespace switch, and no listener before live daemon custody
// can be established independently.
func ServeLocalInherited(ctx context.Context, args []string) error {
	if ctx == nil || ctx.Err() != nil || len(args) != 1 || args[0] != "serve-reviewed" {
		return ErrLocalStartup
	}
	// Check FD 3 before opening the review so a missing inherited descriptor
	// cannot be replaced by a file opened during our startup.
	var st unix.Stat_t
	if unix.Fstat(localDirectoryFD, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return ErrLocalStartup
	}
	dir := os.NewFile(localDirectoryFD, "reviewed-runner-directory")
	if dir == nil {
		return ErrLocalStartup
	}
	defer dir.Close()
	raw, err := securefiles.Read(localReviewRoot+"/review.json", localMaxReviewFile)
	if err != nil {
		return ErrLocalStartup
	}
	review, ok := parseLocalReview(raw, time.Now().UTC())
	if !ok {
		return ErrLocalStartup
	}
	lease, cancel := context.WithDeadline(ctx, review.until)
	defer cancel()
	if !currentLocalDirectoryMount(lease, dir, review) {
		return ErrLocalStartup
	}
	// Kernel-local boot/current namespace/mount evidence is necessary, never
	// sufficient: it cannot show this is the exact live Incus-backed GARM
	// route. There is no independently verified daemon custody producer in this
	// executable. Do not read credentials, bind, sample capacity or serve.
	return ErrLocalDaemonCustody
}
