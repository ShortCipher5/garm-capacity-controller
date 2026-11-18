//go:build linux

package nativeassettransfer

import (
	"encoding/json"
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeassets"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

const maxAttempts = 1024

// Attempt describes exactly one authorized observation, never live runtime state.
// Its sequence is local to the immutable preparation ID and review digest.
type Attempt struct {
	Sequence     uint64                `json:"sequence"`
	Mode         string                `json:"mode"`
	ReviewDigest string                `json:"review_digest"`
	Status       string                `json:"status"`
	StartedAt    time.Time             `json:"started_at"`
	CompletedAt  *time.Time            `json:"completed_at,omitempty"`
	Receipt      *nativeassets.Receipt `json:"receipt,omitempty"`
}

func (s *Service) attemptName(seq uint64, suffix string) string {
	return fmt.Sprintf("%s.attempt.%06d.%s.json", s.plan.Manifest.ID, seq, suffix)
}
func (s *Service) latestSequence() (uint64, error) {
	f, e := os.Open(s.dir)
	if e != nil {
		return 0, ErrInvalid
	}
	defer f.Close()
	names, e := f.Readdirnames(16385)
	if e != nil && e != io.EOF || len(names) > 16384 {
		return 0, ErrInvalid
	}
	prefix := s.plan.Manifest.ID + ".attempt."
	var max, maxResult uint64
	count := 0
	for _, name := range names {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		rest := strings.TrimPrefix(name, prefix)
		if len(rest) < 6 {
			return 0, ErrInvalid
		}
		seq, e := strconv.ParseUint(rest[:6], 10, 64)
		if e != nil || seq < 1 || seq > maxAttempts || fmt.Sprintf("%06d", seq) != rest[:6] {
			return 0, ErrInvalid
		}
		suffix := rest[6:]
		if suffix != ".intent.json" && suffix != ".result.json" {
			return 0, ErrInvalid
		}
		if suffix == ".result.json" && seq > maxResult {
			maxResult = seq
		}
		if suffix == ".intent.json" {
			count++
			if seq > max {
				max = seq
			}
		}
	}
	if uint64(count) != max || maxResult > max {
		return 0, ErrInvalid
	}
	return max, nil
}
func (s *Service) beginAttempt(inspect bool) (Attempt, error) {
	seq, e := s.latestSequence()
	if e != nil || seq >= maxAttempts {
		return Attempt{}, ErrInvalid
	}
	mode := "prepare"
	if inspect {
		mode = "inspect"
	}
	a := Attempt{Sequence: seq + 1, Mode: mode, ReviewDigest: s.ReviewDigest(), Status: "unknown", StartedAt: time.Now().UTC()}
	raw, _ := json.Marshal(a)
	if s.write(s.attemptName(a.Sequence, "intent"), raw) != nil {
		return Attempt{}, ErrUnknown
	}
	return a, nil
}
func (s *Service) finishAttempt(a Attempt, receipt *nativeassets.Receipt) (Attempt, error) {
	a.Status = "observation_unavailable"
	now := time.Now().UTC()
	if now.Before(a.StartedAt) {
		return Attempt{}, ErrUnknown
	}
	a.CompletedAt = &now
	if receipt != nil {
		copy := *receipt
		a.Receipt = &copy
		a.Status = "artifacts_prepared_not_activated"
	}
	raw, _ := json.Marshal(a)
	if s.write(s.attemptName(a.Sequence, "result"), raw) != nil {
		return Attempt{}, ErrUnknown
	}
	return a, nil
}
func (s *Service) latestAttempt() (*Attempt, error) {
	seq, e := s.latestSequence()
	if e != nil {
		return nil, e
	}
	if seq == 0 {
		return nil, nil
	}
	raw, e := s.read(s.attemptName(seq, "intent"))
	if e != nil {
		return nil, ErrInvalid
	}
	var a Attempt
	if decodeJournal(raw, &a) != nil || a.Sequence != seq || a.ReviewDigest != s.ReviewDigest() || (a.Mode != "prepare" && a.Mode != "inspect") || a.Status != "unknown" || a.StartedAt.IsZero() || a.CompletedAt != nil || a.Receipt != nil {
		return nil, ErrInvalid
	}
	raw, e = s.read(s.attemptName(seq, "result"))
	if os.IsNotExist(e) {
		return &a, nil
	}
	if e != nil {
		return nil, ErrInvalid
	}
	var result Attempt
	if decodeJournal(raw, &result) != nil || result.Sequence != a.Sequence || result.ReviewDigest != a.ReviewDigest || result.Mode != a.Mode || !result.StartedAt.Equal(a.StartedAt) || result.CompletedAt == nil || result.CompletedAt.Before(a.StartedAt) {
		return nil, ErrInvalid
	}
	if result.Status == "artifacts_prepared_not_activated" {
		if result.Receipt == nil || nativeassets.ValidateReceipt(s.plan.Manifest, *result.Receipt) != nil {
			return nil, ErrInvalid
		}
	} else if result.Status != "observation_unavailable" || result.Receipt != nil {
		return nil, ErrInvalid
	}
	return &result, nil
}
