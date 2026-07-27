// Package validate owns the shared input preparation path and composes score
// compilation, cast resolution, adapter probing, capability checks, and
// enforcement checks for partitur validate.
//
// Prepare and Run treat the invocation working directory as the repository
// root. The package owns input discovery and application ordering, but not
// diagnostic rendering or CLI exit-code mapping.
package validate
