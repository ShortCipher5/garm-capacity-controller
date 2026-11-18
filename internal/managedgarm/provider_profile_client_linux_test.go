package managedgarm

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/profilesnapshot"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	"testing"
)

func TestProviderProfileSnapshotContinuity(t *testing.T) {
	for _, change := range []string{"rollback", "lineage", "payload"} {
		t.Run(change, func(t *testing.T) {
			c := &providerProfileClient{sequence: -1}
			initial := profilesnapshot.Snapshot{Sequence: 10, Lineage: "original", ProfileDigest: "same"}
			if err := c.acceptSnapshot(initial); err != nil {
				t.Fatal(err)
			}
			next := initial
			next.Sequence++
			if err := c.acceptSnapshot(next); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "rollback":
				next.Sequence = 9
			case "lineage":
				next.Lineage = "restored"
			case "payload":
				next.ProfileDigest = "changed"
			}
			if c.acceptSnapshot(next) == nil {
				t.Fatal("accepted drift")
			}
			initial.Sequence = 12
			if c.acceptSnapshot(initial) == nil {
				t.Fatal("revocation reset")
			}
		})
	}
}

func TestProviderProfileClientRejectsNoncanonicalCredentialPaths(t *testing.T) {
	for _, p := range []string{"relative.pem", "/trusted/link/../client.pem", "/trusted//client.pem"} {
		if _, err := newProviderProfileClient(&Session{}, ProviderProfileConfiguration{TLS: securefiles.TLSFiles{CA: p, Certificate: p, Key: p}}); err == nil {
			t.Fatal("noncanonical privileged path accepted", p)
		}
	}
}
