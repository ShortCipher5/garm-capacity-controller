package incusapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

func TestRuntimeOverridesCannotMintBoundedEvidence(t *testing.T) {
	keys := []string{"raw.lxc", "raw.qemu", "raw.qemu.conf", "raw.qemu.qmp.early", "raw.qemu.qmp.pre-start", "raw.qemu.qmp.post-start", "raw.qemu.scriptlet", "raw.qemu.future"}
	for _, kind := range []string{"container", "virtual-machine"} {
		for _, key := range keys {
			for _, tc := range []struct {
				name   string
				value  any
				accept bool
			}{
				{"empty", "", true}, {"nonempty", "override", false}, {"whitespace", " ", false}, {"null", nil, false}, {"wrong-type", false, false},
			} {
				t.Run(kind+"/"+key+"/"+tc.name, func(t *testing.T) {
					config := map[string]any{"limits.cpu": "2", "limits.memory": "1GiB", key: tc.value}
					raw, err := json.Marshal(map[string]any{"type": kind, "name": "runner-1", "status_code": 103, "expanded_config": config})
					if err != nil {
						t.Fatal(err)
					}
					got, err := parseInstance(raw)
					if tc.accept {
						if err != nil || got.CPUMillis != 2000 || got.RAMMiB != 1024 {
							t.Fatal(got, err)
						}
					} else if !errors.Is(err, ErrUnbounded) || got != (Instance{}) {
						t.Fatal("override accepted", got, err)
					}
				})
			}
		}
	}
}

func TestUnrelatedRawFieldsRemainCompatible(t *testing.T) {
	raw := json.RawMessage(`{"name":"runner-1","status_code":103,"expanded_config":{"limits.cpu":"2","limits.memory":"1GiB","raw.idmap":"both 1000 1000","raw.seccomp":"policy"}}`)
	got, err := parseInstance(raw)
	if err != nil || got.CPUMillis != 2000 || got.RAMMiB != 1024 {
		t.Fatal(got, err)
	}
}

func TestRuntimeOverrideInventoryFailsWithoutPartialResult(t *testing.T) {
	for _, key := range []string{"raw.lxc", "raw.qemu", "raw.qemu.conf", "raw.qemu.qmp.post-start", "raw.qemu.scriptlet"} {
		t.Run(key, func(t *testing.T) {
			server, credentials := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/1.0" {
					writeSync(w, rootMetadata())
					return
				}
				bounded := map[string]any{"name": "bounded", "status_code": 103, "expanded_config": map[string]string{"limits.cpu": "1", "limits.memory": "1GiB"}}
				overridden := map[string]any{"name": "overridden", "status_code": 103, "expanded_config": map[string]string{"limits.cpu": "1", "limits.memory": "1GiB", key: "override"}}
				writeSync(w, []any{bounded, overridden})
			}))
			client, err := New(server.URL, "runners", credentials)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			got, err := client.Instances(context.Background())
			if !errors.Is(err, ErrUnbounded) || got != nil {
				t.Fatal("partial evidence returned", got, err)
			}
		})
	}
}
