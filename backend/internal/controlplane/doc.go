// Package controlplane groups the write-path concerns of Helios: managing the
// definitions that describe how flags behave. It is low-volume, strongly
// consistent, and never sits on the flag-check request path.
//
// Subpackages:
//   - flags:       feature flag and per-environment config CRUD
//   - segments:    reusable targeting segments
//   - experiments: A/B experiment lifecycle
//   - rbac:        server-side role enforcement
//   - audit:       append-only log of every mutation
package controlplane
