// Package managedboundary defines the shared refusal sentinel only. It exposes
// no observation, transport, listener, or runtime authority.
package managedboundary

import "errors"

var ErrBoundary = errors.New("managed stock GARM boundary not established")
