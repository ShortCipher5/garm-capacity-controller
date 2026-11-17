// Copyright 2022 Cloudbase Solutions SRL
//
//    Licensed under the Apache License, Version 2.0 (the "License"); you may
//    not use this file except in compliance with the License. You may obtain
//    a copy of the License at
//
//         http://www.apache.org/licenses/LICENSE-2.0
//
//    Unless required by applicable law or agreed to in writing, software
//    distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
//    WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
//    License for the specific language governing permissions and limitations
//    under the License.

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/cloudbase/garm/cmd/garm-cli/common"
)

// This file implements an opt-in, experimental client for the provisional
// pool settings interface:
//
//	GET   /api/v1/offline/entities/{organization|repository|enterprise}/{entityID}/pools/{poolID}/settings
//	PATCH same path, strict JSON body over {priority, max_runners, min_idle_runners},
//	with a strong quoted ETag carried in If-Match.
//
// The subcommand group is only registered when GARM_CLI_EXPERIMENTAL=1 is
// present in the environment. When disabled, the command tree is byte-for-byte
// identical to stock: this group simply does not exist.
//
// Semantics enforced here:
//   - A 412 response proves our write had no effect. The operator's draft and
//     the fresh ETag from the response are preserved and surfaced.
//   - Any outcome that is ambiguous after dispatch (lost response, transport
//     error, 5xx, unexpected status) is held in an explicit UNKNOWN state,
//     surfaced to the operator, and exits nonzero. We never auto-retry and we
//     never fall back to the stock unconditional pool update.

const (
	experimentalEnvVar             = "GARM_CLI_EXPERIMENTAL"
	experimentalPoolSettingsPrefix = "offline/entities"
	unknownHoldMessage             = `UNKNOWN: the outcome of the last request could not be determined.
The request was dispatched, but the response was lost or ambiguous.
Your draft has been preserved and has NOT been confirmed applied.
Do NOT auto-retry. Re-read the settings (experimental pool-settings get),
reconcile, and only then re-issue a conditional patch.`
)

// poolSettingsEntityTypes are the entity path segments understood by the
// provisional interface.
var poolSettingsEntityTypes = map[string]string{
	"repository":   "repository",
	"org":          "organization",
	"organization": "organization",
	"enterprise":   "enterprise",
}

// poolSettingsOutcome classifies the result of a settings request.
type poolSettingsOutcome int

const (
	outcomeApplied poolSettingsOutcome = iota
	outcomeConflict
	outcomeRejected
	outcomeUnknown
)

func (o poolSettingsOutcome) String() string {
	switch o {
	case outcomeApplied:
		return "applied"
	case outcomeConflict:
		return "conflict"
	case outcomeRejected:
		return "rejected"
	case outcomeUnknown:
		return "unknown"
	}
	return "unknown"
}

// poolSettingsError carries a classified error plus any fresh ETag the server
// returned (set on 412 so the operator can re-read/rebase without losing the
// draft).
type poolSettingsError struct {
	outcome poolSettingsOutcome
	status  int
	etag    string
	msg     string
}

func (e *poolSettingsError) Error() string { return e.msg }

func experimentalEnabled() bool {
	return os.Getenv(experimentalEnvVar) == "1"
}

// poolSettingsRef addresses one pool's settings document.
type poolSettingsRef struct {
	entityType string // repository | organization | enterprise
	entityID   string
	poolID     string
}

func (r poolSettingsRef) path() (string, error) {
	seg, ok := poolSettingsEntityTypes[r.entityType]
	if !ok {
		return "", fmt.Errorf("invalid entity type %q (want repository, organization or enterprise)", r.entityType)
	}
	if r.entityID == "" {
		return "", fmt.Errorf("entity ID must not be empty")
	}
	if r.poolID == "" {
		return "", fmt.Errorf("pool ID must not be empty")
	}
	return fmt.Sprintf("%s/%s/%s/pools/%s/settings",
		experimentalPoolSettingsPrefix, seg, r.entityID, r.poolID), nil
}

// doPoolSettings issues one request against the provisional settings endpoint
// and classifies the outcome. It sends exactly one request; it never retries.
// idempotencyKey is sent as the Idempotency-Key header on writes so a lost
// response can be resolved from the server's durable request receipt.
func doPoolSettings(ctx context.Context, cli *rawClient, method string, ref poolSettingsRef, ifMatch, idempotencyKey string, body io.Reader) (int, []byte, string, error) {
	if cli == nil {
		return 0, nil, "", fmt.Errorf("no HTTP client available")
	}
	path, err := ref.path()
	if err != nil {
		return 0, nil, "", err
	}
	req, err := cli.NewRequest(method, path, body)
	if err != nil {
		return 0, nil, "", err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if ifMatch != "" {
		// Strong quoted ETag, exactly as obtained from GET.
		req.Header.Set("If-Match", ifMatch)
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}

	resp, err := cli.HTTPClient.Do(req.WithContext(ctx))
	if err != nil {
		// Response lost or ambiguous after dispatch: UNKNOWN.
		return 0, nil, "", &poolSettingsError{
			outcome: outcomeUnknown,
			msg:     fmt.Sprintf("request dispatched but the response was lost or ambiguous: %v", err),
		}
	}
	defer resp.Body.Close()

	data, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return resp.StatusCode, nil, "", &poolSettingsError{
			outcome: outcomeUnknown,
			status:  resp.StatusCode,
			etag:    resp.Header.Get("ETag"),
			msg:     fmt.Sprintf("response body could not be read; outcome ambiguous: %v", readErr),
		}
	}

	switch resp.StatusCode {
	case http.StatusOK:
		return resp.StatusCode, data, resp.Header.Get("ETag"), nil
	case http.StatusPreconditionFailed:
		// 412 proves our request had no effect. Surface the fresh ETag so the
		// draft can be rebased onto it after a manual re-read.
		return resp.StatusCode, data, resp.Header.Get("ETag"), &poolSettingsError{
			outcome: outcomeConflict,
			status:  resp.StatusCode,
			etag:    resp.Header.Get("ETag"),
			msg:     "conflict: settings changed since the ETag was issued; your patch was NOT applied",
		}
	case http.StatusConflict:
		// The request identity was already used with different content:
		// definitive refusal, nothing was written. Never an UNKNOWN hold.
		return resp.StatusCode, data, resp.Header.Get("ETag"), &poolSettingsError{
			outcome: outcomeRejected,
			status:  resp.StatusCode,
			etag:    resp.Header.Get("ETag"),
			msg:     fmt.Sprintf("request rejected (status %d): request identity already used with different content; this request was refused and nothing was written: %s", resp.StatusCode, strings.TrimSpace(string(data))),
		}
	case http.StatusBadRequest, http.StatusForbidden, http.StatusPreconditionRequired, http.StatusUnprocessableEntity:
		// Definitive, non-ambiguous rejections.
		return resp.StatusCode, data, resp.Header.Get("ETag"), &poolSettingsError{
			outcome: outcomeRejected,
			status:  resp.StatusCode,
			etag:    resp.Header.Get("ETag"),
			msg:     fmt.Sprintf("request rejected (status %d): %s", resp.StatusCode, strings.TrimSpace(string(data))),
		}
	default:
		// Anything else after dispatch (5xx, redirects, unexpected 2xx) is
		// ambiguous about effect: UNKNOWN hold.
		return resp.StatusCode, data, resp.Header.Get("ETag"), &poolSettingsError{
			outcome: outcomeUnknown,
			status:  resp.StatusCode,
			etag:    resp.Header.Get("ETag"),
			msg:     fmt.Sprintf("unexpected status %d after dispatch; outcome of the write is unknown", resp.StatusCode),
		}
	}
}

// poolSettingsPatch is the strict JSON body for PATCH: exactly the three
// coupled fields, at least one present.
type poolSettingsPatch struct {
	Priority       *uint `json:"priority,omitempty"`
	MaxRunners     *uint `json:"max_runners,omitempty"`
	MinIdleRunners *uint `json:"min_idle_runners,omitempty"`
}

func (p poolSettingsPatch) fields() int {
	n := 0
	if p.Priority != nil {
		n++
	}
	if p.MaxRunners != nil {
		n++
	}
	if p.MinIdleRunners != nil {
		n++
	}
	return n
}

// parsePoolSettingsDraft reads a strict JSON draft: an object whose only
// allowed keys are the three coupled fields, with at least one present.
func parsePoolSettingsDraft(data []byte) (poolSettingsPatch, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return poolSettingsPatch{}, fmt.Errorf("draft is not valid JSON: %w", err)
	}
	var patch poolSettingsPatch
	for key, val := range raw {
		switch key {
		case "priority", "max_runners", "min_idle_runners":
		default:
			return poolSettingsPatch{}, fmt.Errorf("draft contains unsupported field %q (allowed: priority, max_runners, min_idle_runners)", key)
		}
		var u uint
		if err := json.Unmarshal(val, &u); err != nil {
			return poolSettingsPatch{}, fmt.Errorf("draft field %q must be an unsigned integer: %w", key, err)
		}
		switch key {
		case "priority":
			patch.Priority = &u
		case "max_runners":
			patch.MaxRunners = &u
		case "min_idle_runners":
			patch.MinIdleRunners = &u
		}
	}
	if patch.fields() == 0 {
		return poolSettingsPatch{}, fmt.Errorf("draft must set at least one of priority, max_runners, min_idle_runners")
	}
	return patch, nil
}

// wrapPoolSettingsError renders a classified error for the operator while
// preserving its outcome classification for programmatic inspection.
func wrapPoolSettingsError(err error, draft string) error {
	var pse *poolSettingsError
	if errors.As(err, &pse) {
		return &poolSettingsError{
			outcome: pse.outcome,
			status:  pse.status,
			etag:    pse.etag,
			msg:     describePoolSettingsError(err, draft),
		}
	}
	return err
}

func describePoolSettingsError(err error, draft string) string {
	var pse *poolSettingsError
	if !errors.As(err, &pse) {
		return fmt.Sprintf("error: %v", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s", pse.msg)
	if pse.outcome == outcomeConflict {
		fmt.Fprintf(&b, "\nfresh ETag: %s", pse.etag)
		fmt.Fprintf(&b, "\nyour draft has been preserved:\n%s", draft)
		fmt.Fprintf(&b, "\nre-read the settings, reconcile, then re-patch with the fresh ETag.")
	}
	if pse.outcome == outcomeUnknown {
		if draft != "" {
			fmt.Fprintf(&b, "\nyour draft is held and has not been confirmed applied:\n%s", draft)
		}
		fmt.Fprintf(&b, "\n%s", unknownHoldMessage)
	}
	return b.String()
}

func printPoolSettingsGet(body []byte, etag string) {
	if outputFormat == common.OutputFormatJSON {
		printAsJSON(map[string]interface{}{
			"etag":     etag,
			"settings": json.RawMessage(body),
		})
		return
	}
	fmt.Printf("ETag: %s\n", etag)
	fmt.Println(string(body))
}

// parsePoolSettingsReplay recognises the wire document a PATCH answered
// from a stored request receipt carries on HTTP 200 instead of the
// settings tuple (the server does not store the original settings body).
// Only applied outcomes arrive with status 200.
func parsePoolSettingsReplay(body []byte) (outcome string, resultingETag string, ok bool) {
	var doc struct {
		Outcome       string `json:"outcome"`
		ResultingETag string `json:"resulting_etag"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", "", false
	}
	if doc.Outcome != "applied_changed" && doc.Outcome != "applied_noop" {
		return "", "", false
	}
	etag := doc.ResultingETag
	if len(etag) < len(`"0:0"`) || etag[0] != '"' || etag[len(etag)-1] != '"' || !strings.Contains(etag, ":") {
		return "", "", false
	}
	return doc.Outcome, etag, true
}

// validateAppliedSettingsDocument verifies a non-replay 200 body really is
// the settings tuple with a strong quoted ETag. Anything else after a
// dispatched write is UNKNOWN: the write may have committed, and only the
// request receipt (via the recorded request id) can resolve it.
func validateAppliedSettingsDocument(body []byte, etag string) error {
	var doc struct {
		Priority       *uint `json:"priority"`
		MaxRunners     *uint `json:"max_runners"`
		MinIdleRunners *uint `json:"min_idle_runners"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return fmt.Errorf("applied response is not a readable settings document")
	}
	if doc.Priority == nil || doc.MaxRunners == nil || doc.MinIdleRunners == nil {
		return fmt.Errorf("applied response is missing settings fields")
	}
	if len(etag) < len(`"0:0"`) || etag[0] != '"' || etag[len(etag)-1] != '"' || !strings.Contains(etag, ":") {
		return fmt.Errorf("applied response carried no strong quoted ETag")
	}
	return nil
}

// experimentalPendingRecord is the client-side durable journal of the exact
// request that was dispatched while its outcome is not yet known. It is
// written before dispatch and cleared on any definitive outcome, so a process
// restart can recover the pending request and resolve it via the server's
// durable receipt (re-presenting the same request id and payload).
//
// The pending file holds exactly one record: the CLI journal is single-slot
// by design, one in-flight conditional write per CLI instance. Operators
// reconciling several pools at once (for example after a controller
// incident) use the web client, which handles multi-pool recovery; the CLI
// deliberately refuses to queue or overwrite a second pending request.
type experimentalPendingRecord struct {
	RequestID  string `json:"request_id"`
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`
	PoolID     string `json:"pool_id"`
	ETag       string `json:"etag"`
	Payload    string `json:"payload"`
	At         string `json:"at"`
	// Profile and Server bind the record to the exact invocation context
	// that dispatched it: the active profile name and the effective base
	// URL of the client that carried the request. A journaled request is
	// only ever applied or resolved from a matching binding.
	Profile string `json:"profile"`
	Server  string `json:"server"`
}

// experimentalPendingPathOverride exists so tests never touch the real user
// configuration directory.
var experimentalPendingPathOverride string

func experimentalPendingPath() string {
	if experimentalPendingPathOverride != "" {
		return experimentalPendingPathOverride
	}
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "garm-cli", "experimental-pending.json")
}

// writeExperimentalPending durably persists the pending request. It is
// fail-closed and atomic: the record is staged as a temp file in the same
// directory, fsynced, then renamed over the target (0600), so the journal is
// never observed torn and a failure here must abort the write before
// dispatch.
func writeExperimentalPending(rec experimentalPendingRecord) error {
	path := experimentalPendingPath()
	if path == "" {
		return fmt.Errorf("no user configuration directory available for the pending-request journal")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("could not prepare the pending-request journal directory: %w", err)
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".experimental-pending-*.tmp")
	if err != nil {
		return fmt.Errorf("could not stage the pending-request journal: %w", err)
	}
	tmpPath := tmp.Name()
	// Best effort: a no-op once the rename has committed the record.
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("could not write the pending-request journal: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("could not fsync the pending-request journal: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("could not close the pending-request journal: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("could not commit the pending-request journal: %w", err)
	}
	// Make the rename itself durable where the platform allows it.
	if dirHandle, err := os.Open(dir); err == nil {
		_ = dirHandle.Sync()
		_ = dirHandle.Close()
	}
	return nil
}

// loadExperimentalPending returns the journaled unresolved request, if any. A
// path that cannot name a regular journal file (absent, a directory in its
// ancestry, or a directory at the path itself) reports no record. An
// unreadable or corrupt journal is an error: the operator must decide what
// to do with it, never have it silently overwritten by a fresh write.
func loadExperimentalPending() (*experimentalPendingRecord, bool, error) {
	path := experimentalPendingPath()
	if path == "" {
		return nil, false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) || errors.Is(err, syscall.EISDIR) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("could not read the pending-request journal: %w", err)
	}
	var rec experimentalPendingRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, false, fmt.Errorf("the pending-request journal exists but is corrupt: %w", err)
	}
	return &rec, true, nil
}

func clearExperimentalPending() {
	if path := experimentalPendingPath(); path != "" {
		_ = os.Remove(path)
	}
}

func init() {
	registerExperimental(rootCmd)
}

// registerExperimental adds the experimental command tree to root, but only
// when the opt-in environment variable is present. When disabled, root is left
// untouched and the command tree is identical to stock.
func registerExperimental(root *cobra.Command) {
	if experimentalEnabled() {
		root.AddCommand(newExperimentalCmd(rawHTTPClient))
	}
}

var (
	expPoolEntityRepo        string
	expPoolEntityOrg         string
	expPoolEntityEnterprise  string
	expPoolID                string
	expPoolSettingsETag      string
	expPoolSettingsDraftFile string
	expPoolSettingsRequestID string
	expPoolSettingsResolve   bool
	expPriority              uint
	expMaxRunners            uint
	expMinIdleRunners        uint
)

// newExperimentalCmd builds the experimental command tree. A nil cli means the
// package-level rawHTTPClient (resolved from the active profile) is used at
// run time.
func newExperimentalCmd(cli *rawClient) *cobra.Command {
	experimentalCmd := &cobra.Command{
		Use:    "experimental",
		Short:  "Experimental commands (provisional interfaces; opt-in via " + experimentalEnvVar + "=1)",
		Hidden: true,
		Long: `Experimental commands.

These commands speak provisional interfaces that are not part of the stable
API. They are hidden and only registered when ` + experimentalEnvVar + `=1 is
set in the environment. They never fall back to stock unconditional writes.`,
	}

	poolSettingsCmd := &cobra.Command{
		Use:          "pool-settings",
		Short:        "Experimental conditional pool settings editor (priority, max_runners, min_idle_runners)",
		SilenceUsage: true,
	}

	getCmd := &cobra.Command{
		Use:          "get",
		Short:        "Read pool settings and their ETag",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if needsInit {
				return errNeedsInitError
			}
			ref, err := experimentalPoolSettingsRefFromFlags()
			if err != nil {
				return err
			}
			client := cli
			if client == nil {
				client = rawHTTPClient
			}
			_, body, etag, err := doPoolSettings(cmd.Context(), client, http.MethodGet, ref, "", "", nil)
			if err != nil {
				return wrapPoolSettingsError(err, "")
			}
			printPoolSettingsGet(body, etag)
			return nil
		},
	}

	patchCmd := &cobra.Command{
		Use:          "patch",
		Short:        "Conditionally patch pool settings using If-Match",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if needsInit {
				return errNeedsInitError
			}

			// The whole read-modify-write of the single-slot journal — load,
			// binding decision, journaling and the dispatch it guards — runs
			// under one exclusive cross-process advisory lock, so a second
			// garm-cli can never interleave a journal entry or a resolution
			// with this one. --resolve takes the same lock below.
			release, err := acquireExperimentalJournalLock()
			if err != nil {
				return err
			}
			defer release()

			// Read-before-write: any existing pending record is loaded
			// before anything is built or sent. A normal patch invocation
			// never overwrites an unresolved journaled request.
			pending, hasPending, err := loadExperimentalPending()
			if err != nil {
				return err
			}

			client := cli
			if client == nil {
				client = rawHTTPClient
			}
			profile, server := experimentalCurrentBinding(client)

			if expPoolSettingsResolve {
				return runPoolSettingsResolve(cmd, client, pending, hasPending, profile, server)
			}

			ref, err := experimentalPoolSettingsRefFromFlags()
			if err != nil {
				return err
			}
			if ref.poolID == "" {
				return fmt.Errorf("pool ID must not be empty (set --pool)")
			}

			if hasPending {
				if mismatch := pendingBindingMismatch(pending, profile, server, ref); mismatch != "" {
					return fmt.Errorf("refusing to send a new write: pending request %q belongs to %s — switch profile or server, or resolve the pending request first (--resolve re-presents the journaled request)",
						pending.RequestID, mismatch)
				}
				return fmt.Errorf(`refusing to send a new write: unresolved request %q for %s %s pool %s, dispatched at %s, is still pending.
The pending-request journal holds exactly one record and is never overwritten silently.
Resolve it first: re-run this command with --resolve to re-present that exact request against the server's durable receipt; only clear the journal by hand once you have confirmed out-of-band that the request had no effect`,
					pending.RequestID, pending.EntityType, pending.EntityID, pending.PoolID, pending.At)
			}

			// Never dispatch an unconditional write: a strong ETag is mandatory.
			if expPoolSettingsETag == "" {
				return fmt.Errorf("refusing to patch without --etag: unconditional writes are never sent; obtain a strong ETag via 'experimental pool-settings get'")
			}

			patch, draft, err := experimentalPoolSettingsPatchFromFlags(cmd)
			if err != nil {
				return err
			}

			payload, err := json.Marshal(patch)
			if err != nil {
				return err
			}

			// Persistent request identity: supplied by the operator to
			// re-present an earlier request (e.g. to resolve an UNKNOWN
			// outcome from the server's durable receipt), or generated here
			// and reported so a lost response can be correlated later.
			requestID := expPoolSettingsRequestID
			if requestID == "" {
				requestID = uuid.NewString()
			}

			// Journal the exact request before dispatch so an UNKNOWN
			// outcome survives process restart. Fail closed: the write is
			// never sent unjournaled. The record is bound to this
			// invocation's profile/server so it can only ever be resolved
			// from the same context.
			if err := writeExperimentalPending(experimentalPendingRecord{
				RequestID:  requestID,
				EntityType: ref.entityType,
				EntityID:   ref.entityID,
				PoolID:     ref.poolID,
				ETag:       expPoolSettingsETag,
				Payload:    string(payload),
				At:         time.Now().UTC().Format(time.RFC3339),
				Profile:    profile,
				Server:     server,
			}); err != nil {
				return fmt.Errorf("refusing to send the request unjournaled: the pending-request journal could not be written: %w", err)
			}

			_, body, newETag, err := doPoolSettings(cmd.Context(), client, http.MethodPatch, ref, expPoolSettingsETag, requestID, strings.NewReader(string(payload)))
			if err != nil {
				var pse *poolSettingsError
				if errors.As(err, &pse) && pse.outcome != outcomeUnknown {
					// Definitive outcome: nothing to recover.
					clearExperimentalPending()
				}
				return wrapPoolSettingsError(err, draft)
			}
			return reportPoolSettingsPatchOutcome(cmd, requestID, body, newETag, draft)
		},
	}

	for _, c := range []*cobra.Command{getCmd, patchCmd} {
		c.Flags().StringVarP(&expPoolEntityRepo, "repo", "r", "", "Repository entity ID.")
		c.Flags().StringVarP(&expPoolEntityOrg, "org", "o", "", "Organization entity ID.")
		c.Flags().StringVarP(&expPoolEntityEnterprise, "enterprise", "e", "", "Enterprise entity ID.")
		c.Flags().StringVar(&expPoolID, "pool", "", "Pool ID.")
		c.MarkFlagsMutuallyExclusive("repo", "org", "enterprise")
	}
	// get validates its target via annotations. patch cannot: --resolve must
	// run with no entity or pool flags at all, since the pending journal
	// supplies the target, so patch validates its target inside RunE.
	getCmd.MarkFlagRequired("pool") //nolint
	getCmd.MarkFlagsOneRequired("repo", "org", "enterprise")

	patchCmd.Flags().StringVar(&expPoolSettingsETag, "etag", "", "Strong quoted ETag from a previous GET (sent as If-Match). Required; unconditional writes are never sent. Ignored by --resolve.")
	patchCmd.Flags().StringVar(&expPoolSettingsRequestID, "request-id", "", "Persistent request identity sent as Idempotency-Key. Generated when omitted. Allowed on a fresh write; rejected with --resolve, which reuses the journaled request id.")
	patchCmd.Flags().StringVar(&expPoolSettingsDraftFile, "file", "", "JSON draft file with the desired settings (keys: priority, max_runners, min_idle_runners).")
	patchCmd.Flags().UintVar(&expPriority, "priority", 0, "Desired priority.")
	patchCmd.Flags().UintVar(&expMaxRunners, "max-runners", 0, "Desired max runners.")
	patchCmd.Flags().UintVar(&expMinIdleRunners, "min-idle-runners", 0, "Desired min idle runners.")
	patchCmd.Flags().BoolVar(&expPoolSettingsResolve, "resolve", false, `Resolve the pending request held in the client journal: re-send exactly the journaled payload, ETag precondition and request id against its recorded entity/pool, so the server answers from its durable request receipt. Use this after an UNKNOWN outcome. The CLI journal holds at most one pending request (single slot per CLI instance; the web client handles multi-pool recovery). Ignores payload flags, --etag and entity flags; an error to combine with payload flags or --request-id.`)

	poolSettingsCmd.AddCommand(getCmd, patchCmd)
	experimentalCmd.AddCommand(poolSettingsCmd)
	return experimentalCmd
}

// runPoolSettingsResolve is the deliberate resolution path for an UNKNOWN
// outcome: it re-sends exactly the journaled request (payload, ETag
// precondition, request id) against the recorded entity/pool and lets the
// server answer from its durable request receipt. A definitive outcome
// clears the journal; a fresh UNKNOWN keeps it and is reported again. The
// journaled request is authoritative, so payload flags, --etag and
// entity/pool flags are ignored here; combining --resolve with payload flags
// or --request-id is ambiguous and refused. Resolution operates on the
// journaled binding: a record bound to a different profile/server than the
// current invocation is refused, never resolved against the wrong server.
// The caller holds the journal lock for the whole read+decision+apply.
func runPoolSettingsResolve(cmd *cobra.Command, client *rawClient, pending *experimentalPendingRecord, hasPending bool, profile, server string) error {
	for _, f := range []string{"priority", "max-runners", "min-idle-runners", "file", "request-id"} {
		if cmd.Flags().Changed(f) {
			return fmt.Errorf("--resolve cannot be combined with --%s: it re-sends exactly the journaled request; remove the flag to resolve the ambiguity", f)
		}
	}
	if !hasPending {
		return fmt.Errorf("nothing to resolve: the pending-request journal holds no unresolved request")
	}
	if mismatch := pendingBindingMismatchResolve(pending, profile, server); mismatch != "" {
		return fmt.Errorf("refusing to resolve: the pending request belongs to %s — switch to that profile/server or clear the journal by hand once you have confirmed out-of-band that the request had no effect", mismatch)
	}
	rec := *pending
	ref := poolSettingsRef{entityType: rec.EntityType, entityID: rec.EntityID, poolID: rec.PoolID}
	if _, err := ref.path(); err != nil {
		return fmt.Errorf("the pending-request journal records an invalid target: %w", err)
	}

	_, body, newETag, err := doPoolSettings(cmd.Context(), client, http.MethodPatch, ref, rec.ETag, rec.RequestID, strings.NewReader(rec.Payload))
	if err != nil {
		var pse *poolSettingsError
		if errors.As(err, &pse) && pse.outcome != outcomeUnknown {
			// Definitive outcome: nothing left to resolve.
			clearExperimentalPending()
		}
		return wrapPoolSettingsError(err, rec.Payload)
	}
	return reportPoolSettingsPatchOutcome(cmd, rec.RequestID, body, newETag, rec.Payload)
}

// reportPoolSettingsPatchOutcome handles the HTTP 200 path of a dispatched
// PATCH, shared by fresh writes and --resolve: recognise a replay answer
// served from the server's durable request receipt, validate a true applied
// tuple, clear the journal on any definitive outcome, and print the result.
// A 200 body that cannot be trusted keeps the journal held as UNKNOWN.
func reportPoolSettingsPatchOutcome(cmd *cobra.Command, requestID string, body []byte, newETag, draft string) error {
	if replayOutcome, resultingETag, ok := parsePoolSettingsReplay(body); ok {
		// The server answered from a durable request receipt: this exact
		// request already applied earlier and nothing was re-applied.
		// Report the recorded outcome; the operator can re-read
		// deliberately if the document is needed.
		clearExperimentalPending()
		if outputFormat == common.OutputFormatJSON {
			printAsJSON(map[string]interface{}{
				"request_id": requestID,
				"outcome":    replayOutcome,
				"etag":       resultingETag,
				"replayed":   true,
			})
		} else {
			fmt.Printf("request-id: %s\n", requestID)
			fmt.Printf("already applied earlier (replayed from stored request receipt, outcome %s). resulting ETag: %s\n", replayOutcome, resultingETag)
		}
		return nil
	}
	if err := validateAppliedSettingsDocument(body, newETag); err != nil {
		// The write may have committed but the response cannot be
		// trusted. Keep the pending journal: re-running with --resolve
		// settles the outcome from the server's durable receipt.
		return wrapPoolSettingsError(&poolSettingsError{
			outcome: outcomeUnknown,
			status:  http.StatusOK,
			etag:    newETag,
			msg:     fmt.Sprintf("%v; the write may have committed and request id %s can resolve it from the server's durable receipt (re-run with --resolve)", err, requestID),
		}, draft)
	}
	clearExperimentalPending()
	if outputFormat == common.OutputFormatJSON {
		printAsJSON(map[string]interface{}{
			"request_id": requestID,
			"outcome":    outcomeApplied.String(),
			"etag":       newETag,
			"settings":   json.RawMessage(body),
		})
	} else {
		fmt.Printf("request-id: %s\n", requestID)
		fmt.Printf("applied. new ETag: %s\n", newETag)
	}
	return nil
}

func experimentalPoolSettingsRefFromFlags() (poolSettingsRef, error) {
	ref := poolSettingsRef{
		entityType: "repository",
		entityID:   expPoolEntityRepo,
		poolID:     expPoolID,
	}
	if expPoolEntityOrg != "" {
		ref.entityType = "organization"
		ref.entityID = expPoolEntityOrg
	} else if expPoolEntityEnterprise != "" {
		ref.entityType = "enterprise"
		ref.entityID = expPoolEntityEnterprise
	}
	if ref.entityID == "" {
		return poolSettingsRef{}, fmt.Errorf("one of --repo, --org or --enterprise is required")
	}
	return ref, nil
}

// experimentalPoolSettingsPatchFromFlags assembles the strict patch body. At
// least one of the three coupled fields must be set, either via flags or via
// a JSON draft file. The returned draft string is a human-readable copy of
// exactly what the operator asked to apply; it is preserved (printed back) on
// conflict so nothing the operator typed is lost.
func experimentalPoolSettingsPatchFromFlags(cmd *cobra.Command) (poolSettingsPatch, string, error) {
	if expPoolSettingsDraftFile != "" {
		for _, f := range []string{"priority", "max-runners", "min-idle-runners"} {
			if cmd.Flags().Changed(f) {
				return poolSettingsPatch{}, "", fmt.Errorf("--file cannot be combined with --%s", f)
			}
		}
		data, err := os.ReadFile(expPoolSettingsDraftFile)
		if err != nil {
			return poolSettingsPatch{}, "", fmt.Errorf("failed to read draft file: %w", err)
		}
		patch, err := parsePoolSettingsDraft(data)
		if err != nil {
			return poolSettingsPatch{}, "", err
		}
		return patch, string(data), nil
	}

	var patch poolSettingsPatch
	if cmd.Flags().Changed("priority") {
		patch.Priority = &expPriority
	}
	if cmd.Flags().Changed("max-runners") {
		patch.MaxRunners = &expMaxRunners
	}
	if cmd.Flags().Changed("min-idle-runners") {
		patch.MinIdleRunners = &expMinIdleRunners
	}
	if patch.fields() == 0 {
		return poolSettingsPatch{}, "", fmt.Errorf("at least one of --priority, --max-runners or --min-idle-runners is required (or use --file)")
	}
	draftJSON, err := json.Marshal(patch)
	if err != nil {
		return poolSettingsPatch{}, "", err
	}
	return patch, string(draftJSON), nil
}
