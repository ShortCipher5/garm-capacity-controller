package incusapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type bodyDeadlineTransport func(*http.Request) (*http.Response, error)

func (f bodyDeadlineTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type bodyDeadlineReader struct {
	read   func([]byte) (int, error)
	closed bool
}

func (r *bodyDeadlineReader) Read(p []byte) (int, error) { return r.read(p) }
func (r *bodyDeadlineReader) Close() error               { r.closed = true; return nil }

func TestRequestBodyFailurePreservesCancellationWithoutEvidenceOrRetry(t *testing.T) {
	for _, kind := range []string{"cancel-context", "cancel-with-eof", "body-deadline", "body-canceled", "unrelated-read-error"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			body := &bodyDeadlineReader{read: func(p []byte) (int, error) {
				n := copy(p, "partial-secret-body")
				switch kind {
				case "cancel-context":
					cancel()
					return n, ctx.Err()
				case "cancel-with-eof":
					cancel()
					return n, io.EOF
				case "body-deadline":
					return n, context.DeadlineExceeded
				case "body-canceled":
					return n, context.Canceled
				default:
					return n, errors.New("private-reader-error")
				}
			}}
			c := &Client{base: url.URL{Scheme: "https", Host: "synthetic.invalid"}, project: "runners", http: &http.Client{Transport: bodyDeadlineTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodGet {
					t.Fatal("unexpected write")
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body, Request: r}, nil
			})}}
			got, err := c.request(ctx, http.MethodGet, "/1.0", nil, nil)
			if !errors.Is(err, ErrResponse) || got.kind != "" || len(got.metadata) != 0 || got.serverKeyFingerprint != "" || got.operation != "" || calls != 1 || !body.closed {
				t.Fatalf("body failure minted evidence, retried, or lost response classification: %v", err)
			}
			expected := error(nil)
			if kind == "cancel-context" || kind == "cancel-with-eof" || kind == "body-canceled" {
				expected = context.Canceled
			}
			if kind == "body-deadline" {
				expected = context.DeadlineExceeded
			}
			if expected != nil && !errors.Is(err, expected) {
				t.Fatalf("body failure lost context classification: %v", err)
			}
			if expected == nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
				t.Fatal("unrelated body error acquired context classification")
			}
			for _, secret := range []string{"partial-secret-body", "private-reader-error"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatal("private body/read error retained")
				}
			}
		})
	}
}

func TestOversizedBodyStillFailsAsResponseWithoutDeadline(t *testing.T) {
	c := &Client{base: url.URL{Scheme: "https", Host: "synthetic.invalid"}, project: "runners", http: &http.Client{Transport: bodyDeadlineTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(strings.Repeat("x", maxBody+1))), Request: r}, nil
	})}}
	got, err := c.request(context.Background(), http.MethodGet, "/1.0", nil, nil)
	if !errors.Is(err, ErrResponse) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || got.kind != "" {
		t.Fatal("oversized body accepted or misclassified")
	}
}
