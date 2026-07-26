// Package adapter resolves and probes Partitur adapter executables.
//
// The package owns no run state. A Client snapshots the operator environment
// once, uses that snapshot for PATH discovery, and passes it unchanged to every
// adapter process.
package adapter
