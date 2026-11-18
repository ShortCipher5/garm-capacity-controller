//go:build linux

package main

import (
	"encoding/json"
	"io"
)

// This explicit inspection path performs only local private-file reads. It never
// invokes a manager, host helper, native observer, network client or constructor.
// In particular a successful report is not a start permit or a readiness proof.
func checkCapsuleConfig(path string, output io.Writer) error {
	c, err := loadCapsuleControl(path)
	if err != nil {
		return err
	}
	if _, _, err = capsuleTrust(c.Control); err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
		ConfigurationValid   bool   `json:"configuration_valid"`
		TLSRolesValid        bool   `json:"tls_roles_valid"`
		ActivationAuthorized bool   `json:"activation_authorized"`
		HostQualified        bool   `json:"host_qualified"`
		Mode                 string `json:"mode"`
	}{true, true, false, false, "local-read-only"})
}
