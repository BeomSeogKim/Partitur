package cancellation

import (
	"context"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

func TestExecuteRejectsMissingInputs(t *testing.T) {
	store, err := runstore.New(t.TempDir(), faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		store *runstore.Store
		runID string
	}{
		{name: "missing store", runID: "run-1"},
		{name: "missing run id", store: store},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := Execute(context.Background(), test.store, runstate.RunID(test.runID))
			if err == nil || err.Error() != "cancellation requires store and run id" {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
