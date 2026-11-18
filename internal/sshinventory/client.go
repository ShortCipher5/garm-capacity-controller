// Package sshinventory performs one pinned, read-only Linux OS inventory probe.
// It does not install agents, change remote configuration, or grant capacity.
package sshinventory

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/subtle"
	"errors"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	"golang.org/x/crypto/ssh"
)

var ErrInventory = errors.New("authenticated SSH inventory unavailable")

type Config struct {
	Address string
	Port    uint16
	User    string
	// HostKey is an independently verified, exact authorized_keys public-key line.
	// Trust-on-first-use and certificate host keys are deliberately unsupported.
	HostKey       string
	CredentialRef string
}
type KeyResolver func(context.Context, string) (ssh.Signer, error)
type Client struct {
	config     Config
	hostKey    ssh.PublicKey
	algorithms []string
	resolve    KeyResolver
}

var username = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

func New(c Config, resolve KeyResolver) (*Client, error) {
	if net.ParseIP(c.Address) == nil || c.Port == 0 || !username.MatchString(c.User) || len(c.HostKey) > 16384 || c.CredentialRef == "" || len(c.CredentialRef) > 256 || strings.ContainsAny(c.CredentialRef, "\r\n\x00") || resolve == nil {
		return nil, ErrInventory
	}
	key, _, options, rest, e := ssh.ParseAuthorizedKey([]byte(c.HostKey))
	if e != nil || len(options) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return nil, ErrInventory
	}
	var algorithms []string
	switch key.Type() {
	case ssh.KeyAlgoED25519:
		algorithms = []string{ssh.KeyAlgoED25519}
	case ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521:
		algorithms = []string{key.Type()}
	case ssh.KeyAlgoRSA:
		cryptoKey, ok := key.(ssh.CryptoPublicKey)
		if !ok {
			return nil, ErrInventory
		}
		public, ok := cryptoKey.CryptoPublicKey().(*rsa.PublicKey)
		if !ok || public.N.BitLen() < 3072 {
			return nil, ErrInventory
		}
		algorithms = []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}
	default:
		return nil, ErrInventory
	}
	return &Client{config: c, hostKey: key, algorithms: algorithms, resolve: resolve}, nil
}

// PrivateKeyFiles resolves only deployment-provided aliases, not a caller-supplied
// filesystem path. Encrypted keys require a separately designed credential flow.
func PrivateKeyFiles(files map[string]string) KeyResolver {
	owned := map[string]string{}
	for k, v := range files {
		owned[k] = v
	}
	return func(ctx context.Context, ref string) (ssh.Signer, error) {
		if ctx == nil || ctx.Err() != nil {
			return nil, ErrInventory
		}
		path, ok := owned[ref]
		if !ok {
			return nil, ErrInventory
		}
		data, e := securefiles.Read(path, 1<<20)
		if e != nil {
			return nil, ErrInventory
		}
		defer func() {
			for i := range data {
				data[i] = 0
			}
		}()
		signer, e := ssh.ParsePrivateKey(data)
		if e != nil || ctx.Err() != nil {
			return nil, ErrInventory
		}
		return signer, nil
	}
}

type Result struct {
	ObservedAt time.Time `json:"observed_at"`
	Source     string    `json:"source"`
	// These values describe the authenticated OS view, possibly a VM/container.
	// They are NOT proof of physical-host ownership or whole-host free capacity.
	LogicalCPUs               *int64 `json:"logical_cpus,omitempty"`
	RAMMiB                    *int64 `json:"ram_mib,omitempty"`
	AvailableRAMMiB           *int64 `json:"available_ram_mib,omitempty"`
	RootDiskMiB               *int64 `json:"root_disk_mib,omitempty"`
	RootAvailableMiB          *int64 `json:"root_available_mib,omitempty"`
	Complete                  bool   `json:"complete"`
	PhysicalCapacityQualified bool   `json:"physical_capacity_qualified"`
}

// Probe uses an explicit literal IP, one public-key credential, exact host-key
// matching and one fixed read-only command. It does not use ssh-agent, ambient
// SSH configuration, port forwarding, a PTY, passwords, sudo or remote installs.
func (c *Client) Probe(ctx context.Context) (Result, error) {
	if c == nil || ctx == nil || ctx.Err() != nil {
		return Result{}, ErrInventory
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	signer, e := c.resolve(ctx, c.config.CredentialRef)
	if e != nil || signer == nil || ctx.Err() != nil {
		return Result{}, ErrInventory
	}
	address := net.JoinHostPort(c.config.Address, strconv.Itoa(int(c.config.Port)))
	conn, e := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", address)
	if e != nil {
		return Result{}, ErrInventory
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if conn.SetDeadline(deadline) != nil {
		return Result{}, ErrInventory
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()
	config := &ssh.ClientConfig{User: c.config.User, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyAlgorithms: c.algorithms, HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if subtle.ConstantTimeCompare(key.Marshal(), c.hostKey.Marshal()) != 1 {
			return ErrInventory
		}
		return nil
	}}
	connection, channels, requests, e := ssh.NewClientConn(conn, address, config)
	if e != nil {
		return Result{}, ErrInventory
	}
	client := ssh.NewClient(connection, channels, requests)
	defer client.Close()
	session, e := client.NewSession()
	if e != nil {
		return Result{}, ErrInventory
	}
	defer session.Close()
	stdout := &boundedOutput{maximum: 4096, cancel: cancel}
	stderr := &boundedOutput{maximum: 4096, cancel: cancel, discard: true}
	session.Stdout = stdout
	session.Stderr = stderr
	session.Stdin = strings.NewReader(probeScript)
	if e = session.Run("/bin/sh -s"); e != nil || ctx.Err() != nil || stdout.exceeded || stderr.exceeded {
		return Result{}, ErrInventory
	}
	result, e := parse(stdout.data.Bytes())
	if e != nil {
		return Result{}, ErrInventory
	}
	result.ObservedAt = time.Now().UTC()
	return result, nil
}

type boundedOutput struct {
	data              bytes.Buffer
	count, maximum    int
	exceeded, discard bool
	cancel            context.CancelFunc
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > b.maximum-b.count {
		b.exceeded = true
		b.cancel()
		return 0, ErrInventory
	}
	b.count += len(p)
	if !b.discard {
		return b.data.Write(p)
	}
	return len(p), nil
}

// No endpoint, username, path or other caller-controlled text enters this script.
// Absolute tool paths and a fixed PATH avoid remote PATH-dependent tool lookup.
const probeScript = `PATH=/usr/bin:/bin
export PATH
export LC_ALL=C
printf 'garm_inventory_v1\n'
printf 'cpu_online='
/usr/bin/getconf _NPROCESSORS_ONLN 2>/dev/null || printf 'unknown\n'
/usr/bin/awk '/^MemTotal:/ {total=$2} /^MemAvailable:/ {available=$2} END {print "ram_kib=" (total ? total : "unknown"); print "available_ram_kib=" (available != "" ? available : "unknown")}' /proc/meminfo 2>/dev/null || printf 'ram_kib=unknown\navailable_ram_kib=unknown\n'
/usr/bin/df -Pk / 2>/dev/null | /usr/bin/awk 'NR==2 && NF>=6 {print "root_disk_kib=" $2;print "root_available_kib=" $4;found=1} END {if(!found){print "root_disk_kib=unknown";print "root_available_kib=unknown"}}'
`

func parse(data []byte) (Result, error) {
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 6 || lines[0] != "garm_inventory_v1" {
		return Result{}, ErrInventory
	}
	result := Result{Source: "authenticated_linux_os_view"}
	fields := map[string]**int64{"cpu_online": &result.LogicalCPUs, "ram_kib": &result.RAMMiB, "available_ram_kib": &result.AvailableRAMMiB, "root_disk_kib": &result.RootDiskMiB, "root_available_kib": &result.RootAvailableMiB}
	seen := map[string]bool{}
	for _, line := range lines[1:] {
		key, value, ok := strings.Cut(line, "=")
		dst, known := fields[key]
		if !ok || !known || seen[key] {
			return Result{}, ErrInventory
		}
		seen[key] = true
		if value == "unknown" {
			continue
		}
		if value == "" || strings.Trim(value, "0123456789") != "" {
			return Result{}, ErrInventory
		}
		n, e := strconv.ParseInt(value, 10, 64)
		if e != nil || n < 0 || n > 1<<50 {
			return Result{}, ErrInventory
		}
		if key == "cpu_online" {
			if n == 0 || n > 1000000 {
				return Result{}, ErrInventory
			}
		} else {
			if (key == "ram_kib" || key == "root_disk_kib") && n < 1024 {
				return Result{}, ErrInventory
			}
			n /= 1024
		}
		*dst = &n
	}
	if result.RAMMiB != nil && result.AvailableRAMMiB != nil && *result.AvailableRAMMiB > *result.RAMMiB {
		return Result{}, ErrInventory
	}
	if result.RootDiskMiB != nil && result.RootAvailableMiB != nil && *result.RootAvailableMiB > *result.RootDiskMiB {
		return Result{}, ErrInventory
	}
	result.Complete = result.LogicalCPUs != nil && result.RAMMiB != nil && result.AvailableRAMMiB != nil && result.RootDiskMiB != nil && result.RootAvailableMiB != nil
	return result, nil
}
