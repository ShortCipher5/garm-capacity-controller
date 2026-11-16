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
	"strings"

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
func doPoolSettings(ctx context.Context, cli *rawClient, method string, ref poolSettingsRef, ifMatch string, body io.Reader) (int, []byte, string, error) {
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
			_, body, etag, err := doPoolSettings(cmd.Context(), client, http.MethodGet, ref, "", nil)
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
			ref, err := experimentalPoolSettingsRefFromFlags()
			if err != nil {
				return err
			}

			// Never dispatch an unconditional write: a strong ETag is mandatory.
			if expPoolSettingsETag == "" {
				return fmt.Errorf("refusing to patch without --etag: unconditional writes are never sent; obtain a strong ETag via 'experimental pool-settings get'")
			}

			patch, draft, err := experimentalPoolSettingsPatchFromFlags(cmd)
			if err != nil {
				return err
			}

			client := cli
			if client == nil {
				client = rawHTTPClient
			}

			payload, err := json.Marshal(patch)
			if err != nil {
				return err
			}
			_, body, newETag, err := doPoolSettings(cmd.Context(), client, http.MethodPatch, ref, expPoolSettingsETag, strings.NewReader(string(payload)))
			if err != nil {
				return wrapPoolSettingsError(err, draft)
			}
			if replayOutcome, resultingETag, ok := parsePoolSettingsReplay(body); ok {
				// The server answered from a durable request receipt: this
				// exact request already applied earlier and nothing was
				// re-applied. Report the recorded outcome; the operator can
				// re-read deliberately if the document is needed.
				if outputFormat == common.OutputFormatJSON {
					printAsJSON(map[string]interface{}{
						"outcome":         replayOutcome,
						"etag":            resultingETag,
						"replayed":        true,
						"request_outcome": "applied",
					})
				} else {
					fmt.Printf("already applied earlier (replayed from stored request receipt, outcome %s). resulting ETag: %s\n", replayOutcome, resultingETag)
				}
				return nil
			}
			if outputFormat == common.OutputFormatJSON {
				printAsJSON(map[string]interface{}{
					"outcome":  outcomeApplied.String(),
					"etag":     newETag,
					"settings": json.RawMessage(body),
				})
			} else {
				fmt.Printf("applied. new ETag: %s\n", newETag)
			}
			return nil
		},
	}

	for _, c := range []*cobra.Command{getCmd, patchCmd} {
		c.Flags().StringVarP(&expPoolEntityRepo, "repo", "r", "", "Repository entity ID.")
		c.Flags().StringVarP(&expPoolEntityOrg, "org", "o", "", "Organization entity ID.")
		c.Flags().StringVarP(&expPoolEntityEnterprise, "enterprise", "e", "", "Enterprise entity ID.")
		c.Flags().StringVar(&expPoolID, "pool", "", "Pool ID.")
		c.MarkFlagRequired("pool") //nolint
		c.MarkFlagsMutuallyExclusive("repo", "org", "enterprise")
		c.MarkFlagsOneRequired("repo", "org", "enterprise")
	}

	patchCmd.Flags().StringVar(&expPoolSettingsETag, "etag", "", "Strong quoted ETag from a previous GET (sent as If-Match). Required; unconditional writes are never sent.")
	patchCmd.Flags().StringVar(&expPoolSettingsDraftFile, "file", "", "JSON draft file with the desired settings (keys: priority, max_runners, min_idle_runners).")
	patchCmd.Flags().UintVar(&expPriority, "priority", 0, "Desired priority.")
	patchCmd.Flags().UintVar(&expMaxRunners, "max-runners", 0, "Desired max runners.")
	patchCmd.Flags().UintVar(&expMinIdleRunners, "min-idle-runners", 0, "Desired min idle runners.")

	poolSettingsCmd.AddCommand(getCmd, patchCmd)
	experimentalCmd.AddCommand(poolSettingsCmd)
	return experimentalCmd
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
