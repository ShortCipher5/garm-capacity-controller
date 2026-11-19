// Package managedprojection scopes and strips already-read stock responses.
// These pure projections do not authenticate, perform I/O, establish freshness,
// or confer authority. Callers must retain their existing read and role checks.
package managedprojection
