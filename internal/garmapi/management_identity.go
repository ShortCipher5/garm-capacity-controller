package garmapi

import (
	"crypto/sha256"
	"encoding/hex"
)

// ManagementIdentity binds a durable journal to the trusted API destination,
// never to a bearer token. Credential rotation preserves write history. Endpoint
// changes require operator recovery, not silently starting with an empty journal.
func (m *ManagementClient) ManagementIdentity() string {
	hash := sha256.Sum256([]byte("garm-v0.2.1-management\x00" + m.base))
	return hex.EncodeToString(hash[:])
}
