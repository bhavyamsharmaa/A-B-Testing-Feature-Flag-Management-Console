// Package evaluation is the data-plane hot path: it resolves a feature-flag
// check for a given context against an in-memory ruleset, with no datastore
// call on the request path.
package evaluation
