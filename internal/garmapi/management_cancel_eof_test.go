package garmapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
)

type managementCancelTransport func(*http.Request) (*http.Response, error)

func (f managementCancelTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type managementCancelBody struct {
	cancel context.CancelFunc
	closed bool
}

func (b *managementCancelBody) Read(p []byte) (int, error) {
	n := copy(p, `{"id":"synthetic"}`)
	if b.cancel != nil {
		b.cancel()
	}
	return n, io.EOF
}
func (b *managementCancelBody) Close() error { b.closed = true; return nil }

func TestManagementRejectsCanceledEOFWithoutRetry(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPost} {
		for _, canceled := range []bool{false, true} {
			t.Run(method+map[bool]string{false: "/live", true: "/canceled"}[canceled], func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				body := &managementCancelBody{}
				if canceled {
					body.cancel = cancel
				}
				calls := 0
				m := &ManagementClient{base: "https://synthetic.invalid", client: &Client{http: &http.Client{Transport: managementCancelTransport(func(r *http.Request) (*http.Response, error) {
					calls++
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body, Request: r}, nil
				})}}}
				got, err := m.exchange(ctx, method, "/pools/synthetic", nil)
				if calls != 1 || !body.closed {
					t.Fatal("retried or leaked body", calls, body.closed)
				}
				if !canceled {
					if err != nil || got == nil {
						t.Fatal(got, err)
					}
					return
				}
				expected := ErrManagement
				if method != http.MethodGet {
					expected = ErrManagementOutcomeUnknown
				}
				if !errors.Is(err, expected) || got != nil {
					t.Fatal("canceled EOF accepted or incorrectly classified", got, err)
				}
			})
		}
	}
}
