package overheadrefresh

import (
	"context"
	"testing"
)

func TestNoStoreOrFabricatedTargetCanRefresh(t *testing.T) {
	if _, e := Refresh(context.Background(), nil, []Target{{BindingSequence: 1, Endpoint: "https://example.invalid", Kind: "both"}}); e == nil {
		t.Fatal("no canonical store accepted")
	}
	if _, e := Refresh(nil, nil, nil); e == nil {
		t.Fatal("nil context accepted")
	}
}
