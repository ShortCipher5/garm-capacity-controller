package managedgarm

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/profilesnapshot"
)

func TestHistoricalRestartInspection(t *testing.T) {
	for _, tt := range []struct {
		name, want string
		corrupt    func(*testing.T, *bootstrapFiles, restartHistoryReceipt)
	}{
		{name: "intent", want: "restart intent observed"},
		{name: "suspend-pending", want: "suspension requested; no returned suspend hook observed"},
		{name: "suspend-returned", want: "suspend-hook-returned; no completed resume observed"},
		{name: "resume-only", want: "resume-hook-returned"},
		{name: "receipt", want: "historical revalidation receipt observed"},
		{name: "receipt-clock-step", want: "historical revalidation receipt observed"},
		{name: "unexpected-bootstrap-user", corrupt: func(t *testing.T, f *bootstrapFiles, r restartHistoryReceipt) {
			var b bootstrapRecord
			if _, err := readRestartHistoryRecord(f, "bootstrap-record.json", 16384, &b); err != nil {
				t.Fatal(err)
			}
			if err := f.root.Remove("bootstrap-record.json"); err != nil {
				t.Fatal(err)
			}
			b.UserID = "incorrect"
			putHistory(t, f, "bootstrap-record.json", b)
			putHistory(t, f, "controlled-restart-complete.json", r)
		}},
		{name: "wrong-original-process", corrupt: func(t *testing.T, f *bootstrapFiles, r restartHistoryReceipt) {
			r.PID, r.Start = r.Record.OriginalPID, r.Record.OriginalStart
			putHistory(t, f, "controlled-restart-complete.json", r)
		}},
		{name: "gap-before-receipt", corrupt: func(t *testing.T, f *bootstrapFiles, r restartHistoryReceipt) {
			if err := f.root.Remove("controlled-restart-0-confirmed.json"); err != nil {
				t.Fatal(err)
			}
			putHistory(t, f, "controlled-restart-complete.json", r)
		}},
		{name: "mismatched-receipt", corrupt: func(t *testing.T, f *bootstrapFiles, r restartHistoryReceipt) {
			r.Record.Files["api-cert.pem"] = sealedFileIdentity{Inode: 42}
			putHistory(t, f, "controlled-restart-complete.json", r)
		}},
		{name: "torn-receipt", corrupt: func(t *testing.T, f *bootstrapFiles, _ restartHistoryReceipt) {
			if err := f.root.WriteFile("controlled-restart-complete.json", []byte("{"), 0600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := topologyFixture()
			s.AdminSecretDirectory = t.TempDir()
			root, err := os.OpenRoot(s.AdminSecretDirectory)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			f := &bootstrapFiles{root: root, uid: uint32(os.Geteuid())}
			attempt := bootstrapAttempt{Version: "managed-garm-bootstrap/v1", GarmID: s.GarmID, CredentialDigest: strings.Repeat("a", 64)}
			controller := "79b10c5f-c312-4da8-b920-ff22783ab911"
			putHistory(t, f, "bootstrap-attempt.json", attempt)
			putHistory(t, f, "bootstrap-record.json", bootstrapRecord{bootstrapAttempt: attempt, ControllerID: controller})
			state := admission.BrokerGrantState{Config: admission.Config{Hosts: map[string]admission.Host{}}}
			for i := 0; i < 300; i++ {
				state.Config.Hosts[fmt.Sprintf("fixture-host-%03d", i)] = admission.Host{}
			}
			snapshot, err := profilesnapshot.New(controller, strings.Repeat("b", 64), strings.Repeat("c", 64), state, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			snapshotBytes, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			order, err := managedRestartOrder(s)
			if err != nil {
				t.Fatal(err)
			}
			record := restartRecord{Version: "managed-restart/v1", Specification: s, ProfileSnapshot: snapshotBytes, OriginalPID: 123, OriginalStart: 7, Steps: order, State: "prepared",
				Files: map[string]sealedFileIdentity{".": {Inode: 5}}}
			for _, name := range []string{"garm.toml", "api-cert.pem", "api-key.pem"} {
				record.Files[name] = sealedFileIdentity{Inode: 8, Size: 14, Verity: strings.Repeat("d", 64)}
			}
			intent, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if len(intent) <= 16384 {
				t.Fatal("intent did not exceed the legacy bootstrap read limit")
			}
			putHistory(t, f, "controlled-restart-intent.json", record)
			intentHash := hashBytes(intent)
			if tt.name != "intent" {
				putHistory(t, f, "controlled-restart-writer-suspend-pending.json", intentHash)
			}
			if tt.name != "intent" && tt.name != "suspend-pending" {
				putHistory(t, f, "controlled-restart-writer-suspend-confirmed.json", intentHash)
			}
			if tt.name != "intent" && tt.name != "suspend-pending" && tt.name != "suspend-returned" {
				for i, step := range order {
					for _, phase := range []string{"pending", "confirmed"} {
						putHistory(t, f, fmt.Sprintf("controlled-restart-%d-%s.json", i, phase), restartJournalEvent{Intent: intentHash, Index: i, Step: step, Pending: phase == "pending"})
					}
				}
				for _, phase := range []string{"resume-pending", "resume-confirmed"} {
					putHistory(t, f, "controlled-restart-writer-"+phase+".json", intentHash)
				}
			}
			record.Completed, record.State = len(order), "revalidated"
			r := restartHistoryReceipt{Record: record, PID: 123, Start: 8, At: snapshot.IssuedAt.Add(time.Second)}
			switch {
			case tt.name == "receipt-clock-step":
				r.At = snapshot.IssuedAt.Add(-time.Second)
				putHistory(t, f, "controlled-restart-complete.json", r)
			case tt.name == "receipt":
				putHistory(t, f, "controlled-restart-complete.json", r)
			case tt.corrupt != nil:
				tt.corrupt(t, f, r)
			}
			got, err := inspectRestartHistory(f, s)
			if tt.want == "" {
				if err == nil || got != "" {
					t.Fatalf("unexpected %q, %v", got, err)
				}
				return
			}
			if err != nil || !strings.HasPrefix(got, tt.want) {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func putHistory(t *testing.T, f *bootstrapFiles, filename string, v any) {
	t.Helper()
	if err := f.create(filename, v); err != nil {
		t.Fatal(err)
	}
}
