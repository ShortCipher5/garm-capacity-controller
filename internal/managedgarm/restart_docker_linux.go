package managedgarm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// restartDocker is concrete and root-peer authenticated. No user-supplied URL,
// transport, command or Docker verb can be injected into the coordinator.
type restartDocker struct {
	client    *http.Client
	transport *http.Transport
}

func openRestartDocker(socket string) (*restartDocker, error) {
	if os.Geteuid() != 0 || !filepath.IsAbs(socket) || filepath.Clean(socket) != socket || rootControlledAncestors(filepath.Dir(socket)) != nil {
		return nil, ErrBoundary
	}
	st, err := os.Lstat(socket)
	if err != nil || st.Mode()&os.ModeSocket == 0 || st.Mode().Perm()&0002 != 0 {
		return nil, ErrBoundary
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 {
		return nil, ErrBoundary
	}
	tr := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 35 * time.Second, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		c, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
		if err != nil {
			return nil, ErrBoundary
		}
		u, ok := c.(*net.UnixConn)
		if !ok {
			c.Close()
			return nil, ErrBoundary
		}
		raw, err := u.SyscallConn()
		if err != nil {
			c.Close()
			return nil, ErrBoundary
		}
		trusted := false
		err = raw.Control(func(fd uintptr) {
			cred, e := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
			trusted = e == nil && cred.Uid == 0
		})
		if err != nil || !trusted {
			c.Close()
			return nil, ErrBoundary
		}
		return c, nil
	}}
	return &restartDocker{transport: tr, client: &http.Client{Transport: tr, Timeout: 40 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrBoundary }}}, nil
}
func (d *restartDocker) inspect(ctx context.Context, id string) (container, error) {
	if !digest.MatchString(id) {
		return container{}, ErrBoundary
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "http://docker/containers/"+id+"/json", nil)
	if err != nil {
		return container{}, ErrBoundary
	}
	res, err := d.client.Do(req)
	if err != nil {
		return container{}, ErrBoundary
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return container{}, ErrBoundary
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	if err != nil || len(raw) > 4<<20 {
		return container{}, ErrBoundary
	}
	var c container
	if json.Unmarshal(raw, &c) != nil || c.ID != id {
		return container{}, ErrBoundary
	}
	return c, nil
}
func (d *restartDocker) transition(ctx context.Context, step restartStep) error {
	if !digest.MatchString(step.Container) || (step.Action != "stop" && step.Action != "start") {
		return ErrBoundary
	}
	suffix := ""
	if step.Action == "stop" {
		suffix = "?t=30"
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "http://docker/containers/"+step.Container+"/"+step.Action+suffix, bytes.NewReader(nil))
	if err != nil {
		return ErrBoundary
	}
	res, err := d.client.Do(req)
	if err != nil {
		return ErrBoundary
	}
	defer res.Body.Close()
	// 304 (already in desired state) is not evidence that this start operation
	// caused the process observed later; it must not be accepted as our receipt.
	if res.StatusCode != 204 {
		return ErrBoundary
	}
	c, err := d.inspect(ctx, step.Container)
	if err != nil || c.State.Running != (step.Action == "start") {
		return ErrBoundary
	}
	return nil
}
