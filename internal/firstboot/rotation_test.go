package firstboot

import (
	"context"
	"errors"
	"golang.org/x/crypto/ssh"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func rotationFixture(t *testing.T) (RotationRequest, ssh.Signer, ssh.Signer) {
	r, admin := fixture(t)
	next := key(t)
	p := RotationPlan{RequestID: "11111111-1111-4111-8111-111111111111", Installation: r.Plan, InstallationDigest: r.ReviewDigest, ListenerHostKey: r.Plan.Base.HostKey, OldKey: r.Plan.Base.InventoryKey, NewKey: string(ssh.MarshalAuthorizedKey(next.PublicKey())), ProgramSHA256: RotationProgramSHA256(), RestartInventorySessions: true}
	return RotationRequest{Plan: p, ReviewDigest: RotationDigest(p), Artifact: r.Artifact, Phase: "stage"}, admin, next
}
func TestRotationProofBeforeRemovalAndAfterRestart(t *testing.T) {
	r, admin, next := rotationFixture(t)
	for _, failProbe := range []int{0, 1, 2} {
		steps := []string{}
		probes := 0
		_, err := rotateInventoryKey(context.Background(), r, admin, next, func(_ context.Context, q RotationRequest, _ ssh.Signer) (RotationReceipt, error) {
			steps = append(steps, q.Phase)
			return RotationReceipt{}, nil
		}, func(context.Context, RotationPlan, ssh.Signer) error {
			probes++
			steps = append(steps, "probe")
			if probes == failProbe {
				return errors.New("probe failed")
			}
			return nil
		})
		if (err != nil) != (failProbe != 0) {
			t.Fatal(err)
		}
		expected := []string{"stage", "probe", "commit", "probe"}
		if failProbe == 1 {
			expected = expected[:2]
		}
		if !reflect.DeepEqual(steps, expected) {
			t.Fatal(steps)
		}
	}
}
func TestRotationRejectsRoleAndPlanSubstitutionBeforeContact(t *testing.T) {
	r, admin, next := rotationFixture(t)
	for _, change := range []func(*RotationRequest){func(r *RotationRequest) { r.Plan.RestartInventorySessions = false }, func(r *RotationRequest) { r.Plan.NewKey = r.Plan.OldKey }, func(r *RotationRequest) { r.Plan.NewKey = string(ssh.MarshalAuthorizedKey(admin.PublicKey())) }, func(r *RotationRequest) { r.Plan.ProgramSHA256 = strings.Repeat("0", 64) }} {
		bad := r
		change(&bad)
		bad.ReviewDigest = RotationDigest(bad.Plan)
		_, e := rotateInventoryKey(context.Background(), bad, admin, next, func(context.Context, RotationRequest, ssh.Signer) (RotationReceipt, error) {
			t.Fatal("invalid plan contacted host")
			return RotationReceipt{}, nil
		}, nil)
		if e == nil {
			t.Fatal("invalid accepted")
		}
	}
}
func TestRotationJournalUnknownRequiresExplicitRetryAndPreservesReceipt(t *testing.T) {
	r, _, _ := rotationFixture(t)
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	calls := 0
	run := func(context.Context) (RotationReceipt, error) {
		calls++
		if calls == 1 {
			return RotationReceipt{}, errors.New("lost")
		}
		return RotationReceipt{State: "revoked", Digest: r.ReviewDigest, HostKey: r.Plan.ListenerHostKey, OldKeyRemoved: true, SessionsTerminated: true}, nil
	}
	op, e := JournalRotation(context.Background(), dir, r, false, run)
	if e == nil || op.State != "unknown" {
		t.Fatal(op, e)
	}
	if _, e = JournalRotation(context.Background(), dir, r, false, run); e == nil || calls != 1 {
		t.Fatal("automatic retry")
	}
	op, e = JournalRotation(context.Background(), dir, r, true, run)
	if e != nil || op.State != "revoked" || op.Attempts != 2 {
		t.Fatal(op, e)
	}
	op, e = JournalRotation(context.Background(), dir, r, false, run)
	if e != nil || calls != 2 || op.Receipt == nil {
		t.Fatal("completed receipt lost", e)
	}
}
func TestRotationPythonLifecycle(t *testing.T) {
	c := exec.Command("python3", "-m", "unittest", "rotation_test.py")
	out, e := c.CombinedOutput()
	if e != nil {
		t.Fatalf("%v\n%s", e, out)
	}
}

func TestRotationJournalRefusesSymlinkBeforeExecution(t *testing.T) {
	r, _, _ := rotationFixture(t)
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	target := dir + "/other"
	os.WriteFile(target, []byte("untouched"), 0600)
	if e := os.Symlink(target, dir+"/rotation.lock"); e != nil {
		t.Fatal(e)
	}
	_, e := JournalRotation(context.Background(), dir, r, false, func(context.Context) (RotationReceipt, error) {
		t.Fatal("effect before safe journal")
		return RotationReceipt{}, nil
	})
	if e == nil {
		t.Fatal("followed symlink")
	}
	b, _ := os.ReadFile(target)
	if string(b) != "untouched" {
		t.Fatal("changed target")
	}
}
func TestRotationFixedProgramCompilesAndContainsNoPlanData(t *testing.T) {
	r, _, _ := rotationFixture(t)
	if strings.Contains(rotationCommand(), r.Plan.Installation.Base.Address) || strings.Contains(rotationCommand(), r.Plan.NewKey) {
		t.Fatal("plan data entered command")
	}
	c := exec.Command("python3", "-c", "import sys;compile(sys.stdin.read(),'fixed-rotation','exec')")
	c.Stdin = strings.NewReader(rotationSource())
	if out, e := c.CombinedOutput(); e != nil {
		t.Fatalf("%v: %s", e, out)
	}
}

func TestRotationJournalBindsRequestIDToPlan(t *testing.T) {
	r, _, _ := rotationFixture(t)
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	_, _ = JournalRotation(context.Background(), dir, r, false, func(context.Context) (RotationReceipt, error) { return RotationReceipt{}, errors.New("uncertain") })
	changed := r
	changed.Plan.ListenerHostKey = string(ssh.MarshalAuthorizedKey(key(t).PublicKey()))
	changed.ReviewDigest = RotationDigest(changed.Plan)
	if _, e := JournalRotation(context.Background(), dir, changed, true, func(context.Context) (RotationReceipt, error) {
		t.Fatal("same request rebound")
		return RotationReceipt{}, nil
	}); e == nil {
		t.Fatal("request plan mismatch accepted")
	}
}
