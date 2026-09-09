// Package dataplane groups the read-path concerns of Helios: serving flag
// checks within a low, predictable latency budget. It must keep answering even
// when the control plane is unavailable.
//
// Subpackages:
//   - evaluation: resolve a flag for a context against an in-memory ruleset
package dataplane
