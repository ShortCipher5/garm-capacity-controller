// This private reader exposes only bounded container inspection. It is not
// a runtime observation, authentication capability, or Docker mutation client.
package managedgarm

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
)

type dockerInspectionReader struct {
	client    *http.Client
	transport *http.Transport
}
type runningContainer struct{ Id string }

func newDockerInspectionReader(socket string) (*dockerInspectionReader, error) {
	if os.Geteuid() != 0 {
		return nil, ErrBoundary
	}
	info, e := os.Lstat(socket)
	if e != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0002 != 0 {
		return nil, ErrBoundary
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 {
		return nil, ErrBoundary
	}
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		conn, e := (&net.Dialer{}).DialContext(ctx, "unix", socket)
		if e != nil {
			return nil, e
		}
		u, ok := conn.(*net.UnixConn)
		if !ok {
			conn.Close()
			return nil, ErrBoundary
		}
		raw, e := u.SyscallConn()
		if e != nil {
			conn.Close()
			return nil, e
		}
		trusted := false
		err := raw.Control(func(fd uintptr) {
			cred, e := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
			trusted = e == nil && cred.Uid == 0
		})
		if err != nil || !trusted {
			conn.Close()
			return nil, ErrBoundary
		}
		return conn, nil
	}, DisableKeepAlives: true}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrBoundary }}
	return &dockerInspectionReader{client: client, transport: transport}, nil
}
func (d *dockerInspectionReader) close() { d.transport.CloseIdleConnections() }
func (d *dockerInspectionReader) inspect(ctx context.Context, id string) (container, error) {
	var v container
	if !digest.MatchString(id) {
		return v, ErrBoundary
	}
	err := d.get(ctx, "/containers/"+id+"/json", &v)
	return v, err
}
func (d *dockerInspectionReader) running(ctx context.Context) ([]runningContainer, error) {
	var rows []runningContainer
	if d.get(ctx, "/containers/json", &rows) != nil || len(rows) > 1000 {
		return nil, ErrBoundary
	}
	return rows, nil
}
func (d *dockerInspectionReader) get(ctx context.Context, path string, out any) error {
	// Even internal misuse cannot select a mutation or another daemon endpoint.
	if path != "/containers/json" {
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/json")
		if !digest.MatchString(id) || path != "/containers/"+id+"/json" {
			return ErrBoundary
		}
	}
	r, e := http.NewRequestWithContext(ctx, "GET", "http://docker"+path, nil)
	if e != nil {
		return e
	}
	res, e := d.client.Do(r)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return ErrBoundary
	}
	raw, e := io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	if e != nil || len(raw) > 4<<20 {
		return ErrBoundary
	}
	return json.Unmarshal(raw, out)
}
