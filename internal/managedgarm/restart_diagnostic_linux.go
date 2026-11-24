package managedgarm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"syscall"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/profilesnapshot"
)

// InspectRestartHistory reports historical journal structure only. It does not
// contact GARM, Docker, or a writer and does not authorize retry or recovery.
func InspectRestartHistory(spec Specification) (string, error) {
	if os.Geteuid() != 0 || !validSpec(spec) {
		return "", ErrBoundary
	}
	files, err := openBootstrapFiles(spec.AdminSecretDirectory, 0)
	if err != nil {
		return "", ErrBoundary
	}
	defer files.root.Close()
	return inspectRestartHistory(files, spec)
}

type restartHistoryReceipt struct {
	Record restartRecord
	PID    int
	Start  uint64
	At     time.Time
}

func readRestartHistoryRecord(files *bootstrapFiles, name string, limit int64, out any) ([]byte, error) {
	entry, err := files.root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !files.private(entry, false) {
		return nil, ErrBoundary
	}
	file, err := files.root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !files.private(before, false) || !os.SameFile(entry, before) || before.Size() <= 0 || before.Size() > limit {
		return nil, ErrBoundary
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	after, statErr := file.Stat()
	if err != nil || statErr != nil || int64(len(raw)) != before.Size() || !files.private(after, false) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, ErrBoundary
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return nil, ErrBoundary
	}
	current, err := files.root.Lstat(name)
	if err != nil || !files.private(current, false) || !os.SameFile(before, current) {
		return nil, ErrBoundary
	}
	initial, initialOK := before.Sys().(*syscall.Stat_t)
	final, finalOK := current.Sys().(*syscall.Stat_t)
	if !initialOK || !finalOK || initial.Ctim != final.Ctim {
		return nil, ErrBoundary
	}
	canonical, err := json.Marshal(out)
	if err != nil || !bytes.Equal(raw, canonical) {
		return nil, ErrBoundary
	}
	return raw, nil
}

func inspectRestartHistory(files *bootstrapFiles, spec Specification) (string, error) {
	const maxIntent int64 = 8 << 20 // writer bound, not the bootstrap-record limit
	var r restartRecord
	raw, err := readRestartHistoryRecord(files, "controlled-restart-intent.json", maxIntent, &r)
	if err != nil {
		return "", ErrBoundary
	}
	order, err := managedRestartOrder(spec)
	if err != nil || r.Specification != spec || r.Version != "managed-restart/v1" ||
		r.State != "prepared" || r.Completed != 0 || r.Pending || r.OriginalPID <= 1 ||
		r.OriginalStart == 0 || !reflect.DeepEqual(r.Steps, order) {
		return "", ErrBoundary
	}
	var attempt bootstrapAttempt
	var bootstrap bootstrapRecord
	if _, err := readRestartHistoryRecord(files, "bootstrap-attempt.json", 16384, &attempt); err != nil {
		return "", ErrBoundary
	}
	if _, err := readRestartHistoryRecord(files, "bootstrap-record.json", 16384, &bootstrap); err != nil ||
		attempt != bootstrap.bootstrapAttempt || attempt.Version != "managed-garm-bootstrap/v1" ||
		attempt.GarmID != spec.GarmID || !digest.MatchString(attempt.CredentialDigest) || !stockUUID(bootstrap.ControllerID) || bootstrap.UserID != "" {
		return "", ErrBoundary
	}
	var envelope struct {
		Nonce    string    `json:"nonce"`
		IssuedAt time.Time `json:"issued_at"`
	}
	if json.Unmarshal(r.ProfileSnapshot, &envelope) != nil {
		return "", ErrBoundary
	}
	// Use the recorded time only to check the historical envelope's structure.
	profiles, err := profilesnapshot.Decode(r.ProfileSnapshot, bootstrap.ControllerID, envelope.Nonce, envelope.IssuedAt)
	if err != nil {
		return "", ErrBoundary
	}
	names, err := providerNames(profiles.Payload.Profiles, bootstrap.ControllerID)
	if err != nil || validateSealedConfigSet(r.Files, names) != nil {
		return "", ErrBoundary
	}
	for name, id := range r.Files {
		if id.Inode == 0 || (name == "." && (id.Size != 0 || id.Verity != "")) ||
			(name != "." && (id.Size < 0 || !digest.MatchString(id.Verity))) {
			return "", ErrBoundary
		}
	}
	h := hashBytes(raw)
	gap := false
	participantReturned := false
	check := func(name string, out, expected any) (bool, error) {
		_, err := readRestartHistoryRecord(files, name, 16384, out)
		if errors.Is(err, os.ErrNotExist) {
			gap = true
			return false, nil
		}
		if err != nil || gap || !reflect.DeepEqual(out, expected) {
			return false, ErrBoundary
		}
		return true, nil
	}
	writer := func(phase string) (bool, error) {
		var got string
		return check("controlled-restart-writer-"+phase+".json", &got, &h)
	}
	for _, p := range []string{"suspend-pending", "suspend-confirmed"} {
		if _, err := writer(p); err != nil {
			return "", err
		}
	}
	for i, step := range order {
		for _, phase := range []string{"pending", "confirmed"} {
			var got restartJournalEvent
			want := restartJournalEvent{Intent: h, Index: i, Step: step, Pending: phase == "pending"}
			if _, err := check(fmt.Sprintf("controlled-restart-%d-%s.json", i, phase), &got, &want); err != nil {
				return "", err
			}
		}
	}
	if _, err := writer("resume-pending"); err != nil {
		return "", err
	}
	participantReturned, err = writer("resume-confirmed")
	if err != nil {
		return "", err
	}
	var receipt restartHistoryReceipt
	_, err = readRestartHistoryRecord(files, "controlled-restart-complete.json", maxIntent+4096, &receipt)
	if errors.Is(err, os.ErrNotExist) {
		if participantReturned {
			return "resume-hook-returned; no completion receipt observed", nil
		}
		return "restart intent observed; completion not established", nil
	}
	r.Completed, r.State = len(order), "revalidated"
	if err != nil || gap || !reflect.DeepEqual(r, receipt.Record) || receipt.PID <= 1 ||
		receipt.Start == 0 || (receipt.PID == r.OriginalPID && receipt.Start == r.OriginalStart) ||
		receipt.At.IsZero() || receipt.At.Location() != time.UTC {
		return "", ErrBoundary
	}
	return "historical revalidation receipt observed; current readiness not checked", nil
}
