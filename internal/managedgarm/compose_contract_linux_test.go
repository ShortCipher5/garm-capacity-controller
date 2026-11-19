package managedgarm

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// This adapter projects generated configuration, NOT Docker/kernel observation.
// It must never call Observe or create an Observation/Session. Real image
// defaults, executable existence, mount permissions and listeners remain untested.
func TestGeneratedComposeMatchesPureTopologyContract(t *testing.T) {
	spec, _ := topologyFixture()
	prep := Preparation{Directory: "/private/deployment", GarmImage: spec.GarmImage, ControllerImage: spec.ControllerImage, IngressImage: spec.IngressImage}
	raw, err := json.Marshal(composeDocument(prep, strings.Repeat("1", 64)))
	if err != nil {
		t.Fatal(err)
	}
	if err := checkGeneratedCompose(raw, spec); err != nil {
		t.Fatal(err)
	}
}

func checkGeneratedCompose(raw []byte, spec Specification) error {
	return checkGeneratedComposePublication(raw, spec, false)
}

func checkGeneratedComposePublication(raw []byte, spec Specification, omit bool) error {
	type volume struct {
		Type, Source, Target string
		ReadOnly             bool `json:"read_only"`
		Bind                 struct {
			CreateHostPath *bool `json:"create_host_path"`
		}
	}
	type service struct {
		Image, User                                              string
		Entrypoint, Command, Environment, Profiles, Ports, Tmpfs []string
		WorkingDir                                               string   `json:"working_dir"`
		NetworkMode                                              string   `json:"network_mode"`
		ReadOnly                                                 bool     `json:"read_only"`
		CapDrop                                                  []string `json:"cap_drop"`
		SecurityOpt                                              []string `json:"security_opt"`
		Restart                                                  string
		PidsLimit                                                int             `json:"pids_limit"`
		Privileged                                               json.RawMessage `json:"privileged"`
		CapAdd                                                   json.RawMessage `json:"cap_add"`
		PID                                                      json.RawMessage `json:"pid"`
		IPC                                                      json.RawMessage `json:"ipc"`
		Devices                                                  json.RawMessage `json:"devices"`
		Volumes                                                  []volume
	}
	var doc struct{ Services map[string]service }
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	if len(doc.Services) != 3 {
		return fmt.Errorf("unexpected services")
	}
	var projected [3]container
	for i, name := range []string{"garm", "controller", "ingress"} {
		v, ok := doc.Services[name]
		if !ok {
			return fmt.Errorf("missing role: %s", name)
		}
		if !reflect.DeepEqual(v.Profiles, []string{"managed-control-plane"}) || v.Restart != "no" || v.PidsLimit != 128 || !reflect.DeepEqual(v.Tmpfs, []string{"/tmp:rw,noexec,nosuid,nodev,size=64m"}) {
			return fmt.Errorf("unexpected launch contract: %s", name)
		}
		if v.Entrypoint == nil || len(v.Entrypoint) != 0 || v.Privileged != nil || v.CapAdd != nil || v.PID != nil || v.IPC != nil || v.Devices != nil {
			return fmt.Errorf("unexpected inherited entrypoint or extra privilege fields: %s", name)
		}
		if len(v.Command) == 0 {
			return fmt.Errorf("missing command: %s", name)
		}
		c := &projected[i]
		c.ID = []string{spec.GarmID, spec.ControllerID, spec.IngressID}[i]
		c.Image, c.Config.User = v.Image, v.User
		c.Config.Env, c.Config.Entrypoint, c.Config.Cmd = v.Environment, v.Entrypoint, v.Command
		c.Config.WorkingDir = v.WorkingDir
		c.Path, c.Args = v.Command[0], v.Command[1:]
		// Synthetic runtime placeholders let the pure validator compare the plan.
		// These are not assertions that a container exists or is running.
		c.State.Running, c.State.Pid = true, 100+i
		c.HostConfig.ReadonlyRootfs, c.HostConfig.CapDrop, c.HostConfig.SecurityOpt = v.ReadOnly, v.CapDrop, v.SecurityOpt
		if i == 0 {
			expectedPorts := []string{"8443:8443"}
			if omit {
				expectedPorts = nil
			}
			if v.NetworkMode != "" || !reflect.DeepEqual(v.Ports, expectedPorts) {
				return fmt.Errorf("unexpected public binding")
			}
			c.HostConfig.NetworkMode = "fixture_default"
			if !omit {
				c.HostConfig.PortBindings = map[string][]struct{ HostIP, HostPort string }{"8443/tcp": {{"", "8443"}}}
			}
		} else {
			if v.NetworkMode != "service:garm" || len(v.Ports) != 0 {
				return fmt.Errorf("unexpected namespace: %s", name)
			}
			c.HostConfig.NetworkMode = "container:" + spec.GarmID
		}
		for _, m := range v.Volumes {
			if m.Type != "bind" || (m.Bind.CreateHostPath == nil || *m.Bind.CreateHostPath) {
				return fmt.Errorf("unexpected volume: %s", name)
			}
			c.Mounts = append(c.Mounts, struct {
				Source, Destination string
				RW                  bool
			}{m.Source, m.Target, !m.ReadOnly})
		}
	}
	if err := validateContainers(spec, projected); err != nil {
		return fmt.Errorf("generated plan diverges from pure topology contract: %w", err)
	}
	return nil
}

func TestGeneratedComposeRejectsDroppedOrAdditionalFields(t *testing.T) {
	spec, _ := topologyFixture()
	prep := Preparation{Directory: "/private/deployment", GarmImage: spec.GarmImage, ControllerImage: spec.ControllerImage, IngressImage: spec.IngressImage}
	for _, field := range []string{"privileged", "cap_add", "pid", "ipc", "devices", "entrypoint-missing", "entrypoint-null", "bind-missing", "bind-true"} {
		t.Run(field, func(t *testing.T) {
			doc := composeDocument(prep, strings.Repeat("1", 64))
			role := doc["services"].(map[string]any)["garm"].(map[string]any)
			switch field {
			case "entrypoint-missing":
				delete(role, "entrypoint")
			case "entrypoint-null":
				role["entrypoint"] = nil
			case "bind-missing", "bind-true":
				bind := role["volumes"].([]any)[0].(map[string]any)["bind"].(map[string]any)
				if field == "bind-missing" {
					delete(bind, "create_host_path")
				} else {
					bind["create_host_path"] = true
				}
			default:
				role[field] = nil // Presence itself is outside the generated contract.
			}
			raw, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if checkGeneratedCompose(raw, spec) == nil {
				t.Fatal("tampered generated field accepted")
			}
		})
	}
}

func TestPreparationPublicationOptionChangesOnlyDeclaredPort(t *testing.T) {
	spec, _ := topologyFixture()
	p := Preparation{Directory: "/private/deployment", GarmImage: spec.GarmImage, ControllerImage: spec.ControllerImage, IngressImage: spec.IngressImage}
	defaultDoc := composeDocument(p, "fixture")
	for _, omit := range []bool{false, true} {
		p.OmitIngressPublication = omit
		doc := composeDocument(p, "fixture")
		garm := doc["services"].(map[string]any)["garm"].(map[string]any)
		_, hasPorts := garm["ports"]
		if hasPorts == omit {
			t.Fatal("wrong publication presence")
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if err := checkGeneratedComposePublication(raw, spec, omit); err != nil {
			t.Fatal(err)
		}
		metadata, err := json.Marshal(preparationMetadata(p, "fixture", strings.Repeat("1", 64)))
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if json.Unmarshal(metadata, &fields) != nil {
			t.Fatal("metadata")
		}
		value, present := fields["omit_ingress_publication"]
		if present != omit || (omit && value != true) {
			t.Fatal("publication intent not recorded")
		}
		if fields["Version"] != "managed-garm/v1" {
			t.Fatal("unexpected manifest version")
		}
		if omit {
			garm["ports"] = []string{"8443:8443"}
		}
		if !reflect.DeepEqual(doc, defaultDoc) {
			t.Fatal("option changed unrelated launch fields")
		}
	}
}
