// Package experiments implements control-plane lifecycle management for A/B
// experiments: creation, start and stop. An experiment belongs to one
// environment and one flag, and at most one per flag per environment runs at
// a time. Every state change writes its audit row in the same transaction.
//
// Planned, not built yet: event ingestion (/track) and the results readout.
package experiments
