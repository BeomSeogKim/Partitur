// Package cast resolves Partitur cast layers and evaluates their deterministic
// score, capability, and enforcement rules.
//
// Resolve accepts already-discovered layers in precedence order, highest
// first. An empty slice resolves to an empty v0.1 cast. That cast is valid by
// itself; ValidateScore reports missing bindings only when a score actually
// declares parts.
package cast
