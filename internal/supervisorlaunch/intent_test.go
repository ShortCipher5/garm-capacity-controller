package supervisorlaunch

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

func validClaims() Claims {
	return Claims{Protocol: Protocol, Operation: Operation, Profile: Profile, Scope: strings.Repeat("1", 64), Attempt: strings.Repeat("2", 32), InitArtifactSHA256: strings.Repeat("3", 64), WorkspaceRef: strings.Repeat("4", 64), PayloadRef: strings.Repeat("5", 64)}
}
func encoded(t *testing.T, c Claims) []byte {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func assertZero(t *testing.T, intent Intent, err error) {
	t.Helper()
	if !errors.Is(err, ErrIntent) || intent.Parsed() || intent.Claims() != (Claims{}) || intent.Canonical() != nil || intent.Digest() != "" || intent.Requirements() != (NativeRequirements{}) || intent.ActivationAuthorized() || intent.ExecutionAuthority() != "none" {
		t.Fatalf("nonzero failed intent: %+v %v", intent, err)
	}
}

func TestLaunchIntentCanonicalImmutableDataOnly(t *testing.T) {
	input := encoded(t, validClaims())
	intent, err := Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if !intent.Parsed() || intent.ActivationAuthorized() || intent.ExecutionAuthority() != "none" {
		t.Fatal("unexpected authority")
	}
	if !bytes.Equal(intent.Canonical(), input) || intent.Claims() != validClaims() {
		t.Fatal("canonical mismatch")
	}
	digest := sha256.Sum256(input)
	if intent.Digest() != hex.EncodeToString(digest[:]) {
		t.Fatal("digest mismatch")
	}
	input[0] = 'x'
	copyBytes := intent.Canonical()
	copyBytes[0] = 'x'
	copyClaims := intent.Claims()
	copyClaims.Profile = "legacy-host"
	if intent.Canonical()[0] != '{' || intent.Claims().Profile != Profile {
		t.Fatal("mutable intent")
	}
	requirements := intent.Requirements()
	requirements.Fallback = "host-exec"
	if intent.Requirements().Fallback != "never" || intent.Requirements().Qualification != "unimplemented" {
		t.Fatal("mutable requirements")
	}
}

func TestLaunchIntentInventedClaimsAreNotQualification(t *testing.T) {
	claims := validClaims()
	claims.InitArtifactSHA256 = strings.Repeat("0", 64)
	intent, err := Compile(encoded(t, claims))
	if err != nil {
		t.Fatal(err)
	}
	// Even an invented digest parses. No verifier, enrollment or native capability
	// is hidden in a successful syntax result or a canonical hash.
	if !intent.Parsed() || intent.ActivationAuthorized() || intent.Requirements().Qualification != "unimplemented" || intent.ExecutionAuthority() != "none" {
		t.Fatal("claim promoted to authority")
	}
	claims.Attempt = strings.Repeat("a", 32)
	other, err := Compile(encoded(t, claims))
	if err != nil {
		t.Fatal(err)
	}
	if other.Digest() == intent.Digest() || other.ActivationAuthorized() {
		t.Fatal("changed attempt became an execution permit")
	}
}

func TestLaunchIntentRefusesFallbackAndArbitraryLaunchInputs(t *testing.T) {
	base := encoded(t, validClaims())
	for _, key := range []string{"fallback", "allow_fallback", "backend", "executable", "path", "argv", "env", "cwd", "uid", "gid", "cgroup_fd", "pidfd", "pid", "mounts", "namespaces", "isolated", "qualified", "proof", "activation_authorized", "execution_authority"} {
		t.Run(key, func(t *testing.T) {
			input := append(append([]byte(nil), base[:len(base)-1]...), []byte(`,"`+key+`":true}`)...)
			intent, err := Compile(input)
			assertZero(t, intent, err)
		})
	}
	for _, profile := range []string{"legacy-host", "docker", "fork_then_move", "linux-protected-init-v2", "", strings.ToUpper(Profile)} {
		claims := validClaims()
		claims.Profile = profile
		intent, err := Compile(encoded(t, claims))
		assertZero(t, intent, err)
	}
	claims := validClaims()
	claims.Operation = "launch_now"
	intent, err := Compile(encoded(t, claims))
	assertZero(t, intent, err)
}

func TestLaunchIntentStrictWireRefusal(t *testing.T) {
	base := encoded(t, validClaims())
	cases := [][]byte{nil, {}, []byte("null"), []byte("[]"), []byte("true"), []byte("{"), bytes.Repeat([]byte(" "), MaxIntentBytes+1), append(append([]byte(nil), base...), []byte(" {}")...), append(append([]byte(nil), base...), byte(0xff))}
	for _, name := range []string{"protocol", "operation", "profile", "scope", "attempt", "init_artifact_sha256", "workspace_ref", "payload_ref"} {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(base, &obj); err != nil {
			t.Fatal(err)
		}
		delete(obj, name)
		missing, _ := json.Marshal(obj)
		cases = append(cases, missing)
		for _, value := range []string{"null", "true", "1", "1.0", "[]", "{}"} {
			if err := json.Unmarshal(base, &obj); err != nil {
				t.Fatal(err)
			}
			obj[name] = json.RawMessage(value)
			bad, _ := json.Marshal(obj)
			cases = append(cases, bad)
		}
		duplicate := append(append([]byte(nil), base[:len(base)-1]...), []byte(`,"`+name+`":"duplicate"}`)...)
		cases = append(cases, duplicate)
		if err := json.Unmarshal(base, &obj); err != nil {
			t.Fatal(err)
		}
		obj[strings.ToUpper(name)] = obj[name]
		delete(obj, name)
		badCase, _ := json.Marshal(obj)
		cases = append(cases, badCase)
	}
	for index, input := range cases {
		t.Run(strconv.Itoa(index), func(t *testing.T) { intent, err := Compile(input); assertZero(t, intent, err) })
	}
}

func TestLaunchIntentIdentifierRefusalAndWhitespaceCanonicalization(t *testing.T) {
	for _, field := range []string{"scope", "attempt", "init_artifact_sha256", "workspace_ref", "payload_ref"} {
		for _, bad := range []string{"", "../ref", "/ref", strings.Repeat("a", 31), strings.Repeat("G", 64), strings.Repeat("a", 65), "\x00", strings.Repeat("a", 63) + "A"} {
			var obj map[string]string
			json.Unmarshal(encoded(t, validClaims()), &obj)
			obj[field] = bad
			b, _ := json.Marshal(obj)
			intent, err := Compile(b)
			assertZero(t, intent, err)
		}
	}
	var obj map[string]string
	json.Unmarshal(encoded(t, validClaims()), &obj)
	indented, err := json.MarshalIndent(obj, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	intent, err := Compile(indented)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := Compile(encoded(t, validClaims()))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(intent.Canonical(), plain.Canonical()) || intent.Digest() != plain.Digest() {
		t.Fatal("noncanonical claims")
	}
	var zero Intent
	if zero.Parsed() || zero.Canonical() != nil || zero.Digest() != "" || zero.ActivationAuthorized() || zero.ExecutionAuthority() != "none" {
		t.Fatal("zero authority")
	}
}

// This is a narrow source contract, not an OS isolation proof. A future native
// executor needs its own review instead of silently appearing as a fallback.
func TestLaunchIntentHasNoExecutionPortOrOperatingSystemImports(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"bytes": true, "crypto/sha256": true, "encoding/hex": true, "encoding/json": true, "errors": true, "io": true, "unicode/utf8": true}
	exports := map[string]bool{"Compile": true, "Parsed": true, "Claims": true, "Canonical": true, "Digest": true, "ActivationAuthorized": true, "ExecutionAuthority": true, "Requirements": true}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), entry.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imported := range file.Imports {
			path, err := strconv.Unquote(imported.Path.Value)
			if err != nil || !allowed[path] {
				t.Fatalf("unreviewed import %s", imported.Path.Value)
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			if function, ok := node.(*ast.FuncDecl); ok && ast.IsExported(function.Name.Name) && !exports[function.Name.Name] {
				t.Errorf("unreviewed execution/interface port %s", function.Name.Name)
			}
			return true
		})
	}
}
