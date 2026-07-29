// Package cancellation executes the single cancellation oracle in DESIGN §6.
package cancellation

import (
	"context"
	"errors"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

// Execute runs DESIGN §6's cancellation oracle for a nonterminal cancelling
// run. Callers are responsible for selecting the cancellation path; this
// package owns its ordered effects so recovery, the CLI, and the driver cannot
// diverge.
func Execute(ctx context.Context, store *runstore.Store, runID runstate.RunID) error {
	if store == nil || runID == "" {
		return errors.New("cancellation requires store and run id")
	}
	sweep, err := store.SweepCancellationSessions(ctx, runID)
	if err != nil {
		return err
	}
	store.Reached(faultpoint.PointCancelSessionsSwept)
	return store.ExecuteCancellation(runID, sweep)
}
