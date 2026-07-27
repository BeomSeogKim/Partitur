// Package adapter resolves, probes, and executes Partitur adapter processes.
//
// The package owns no journal. Run execution receives durable append callbacks
// from its caller and validates their receipts before crossing each §4
// boundary. A Client snapshots the operator environment once, uses that
// snapshot for PATH discovery, and passes it unchanged to every adapter
// process.
package adapter
