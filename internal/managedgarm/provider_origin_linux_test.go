package managedgarm

import (
	"context"
	"testing"
)

func TestProviderOriginCannotUseArbitraryEndpointOrMissingSession(t *testing.T) {
	for _, cfg := range []*ProviderOriginConfiguration{nil, {Endpoint: "http://127.0.0.1:9445"}, {Endpoint: "https://remote.invalid:9445"}, {Endpoint: "https://127.0.0.1:9445/path"}} {
		if _, err := observeProviderOrigin(context.Background(), &Session{}, cfg); err == nil {
			t.Fatal("unverified origin accepted")
		}
	}
	if _, err := observeProviderOrigin(context.Background(), nil, &ProviderOriginConfiguration{Endpoint: "https://127.0.0.1:9445"}); err == nil {
		t.Fatal("missing actual session accepted")
	}
}
