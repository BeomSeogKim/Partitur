package docclause

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A clause cut by a packet boundary is classified in both packets under one marker ID, and
// `orderedClassifications` collapses the two ranges before enforcing document-wide anchor
// uniqueness. That collapse depends on exact byte adjacency, so a receipt whose final range stops
// short of the region's trailing newline produces two same-ID ranges instead of one.
//
// Nothing catches that until baseline activation at 33/33, which is many packets away from the
// receipt that caused it. This runs the same merge over the reviewed receipts that exist today.
func TestConfirmedReceiptsMergeWithoutDuplicateAnchors(t *testing.T) {
	documentPath := filepath.Join("..", "..", "docs", "DESIGN.md")
	document, err := os.ReadFile(documentPath)
	if err != nil {
		t.Fatal(err)
	}
	regions, err := GenerateRegions(document, GitBlobID(document))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "DESIGN.clause-staging.json"))
	if err != nil {
		t.Fatal(err)
	}
	var registry Registry
	if err := json.Unmarshal(raw, &registry); err != nil {
		t.Fatal(err)
	}
	reviewed := make(map[string]bool, len(registry.Receipts))
	for _, receipt := range registry.Receipts {
		reviewed[regionKey(receipt.Key)] = receipt.Review != nil
	}
	var present []Region
	for _, region := range regions {
		if reviewed[regionKey(region.Key)] {
			present = append(present, region)
		}
	}
	if len(present) == 0 {
		t.Fatal("no reviewed regions to merge")
	}
	merged, err := orderedClassifications(present, registry)
	if err != nil {
		t.Fatalf("reviewed receipts do not merge: %v", err)
	}

	// The uniqueness check inside `orderedClassifications` already rejects a failed merge, so
	// counting IDs afterwards proves nothing it did not. What needs asserting is the positive
	// fact the merge depends on: that two adjacent packets really do carry one clause under one
	// ID, and that the merged range spans both. Without this a future packet could open a fresh
	// ID for its continuation and every check here would still pass.
	continuations := []struct {
		markerID  string
		firstOrd  int
		secondOrd int
	}{
		{"adapter-transport.framing", 5, 6},
		{"validation-probe.per-adapter-once", 6, 7},
		{"composition-failure.causes-closed-by-appendix-d", 9, 10},
	}
	for _, want := range continuations {
		var tailEnd, headStart int
		for _, receipt := range registry.Receipts {
			if receipt.Review == nil {
				continue
			}
			decisions := receipt.Review.Decisions
			switch receipt.Key.Ordinal {
			case want.firstOrd:
				last := decisions[len(decisions)-1]
				if last.MarkerID != want.markerID || last.Kind != ClassificationAnchor {
					t.Fatalf("packet %d final decision = %q/%s, want anchor %q",
						want.firstOrd, last.MarkerID, last.Kind, want.markerID)
				}
				tailEnd = last.EndByte
			case want.secondOrd:
				first := decisions[0]
				if first.MarkerID != want.markerID || first.Kind != ClassificationAnchor {
					t.Fatalf("packet %d first decision = %q/%s, want anchor %q",
						want.secondOrd, first.MarkerID, first.Kind, want.markerID)
				}
				headStart = first.StartByte
			}
		}
		if tailEnd == 0 || headStart == 0 {
			t.Fatalf("continuation %q: one side is missing a receipt", want.markerID)
		}
		if tailEnd != headStart {
			t.Fatalf("continuation %q is not byte-adjacent: packet %d ends at %d, packet %d starts at %d",
				want.markerID, want.firstOrd, tailEnd, want.secondOrd, headStart)
		}
		found := 0
		for _, classification := range merged {
			if classification.MarkerID == want.markerID {
				found++
				if classification.EndByte <= tailEnd {
					t.Fatalf("continuation %q merged to [%d,%d), which does not span past the packet edge at %d",
						want.markerID, classification.StartByte, classification.EndByte, tailEnd)
				}
			}
		}
		if found != 1 {
			t.Fatalf("continuation %q survives merge as %d ranges, want 1", want.markerID, found)
		}
	}

	anchors := 0
	for _, classification := range merged {
		if classification.Kind == ClassificationAnchor {
			anchors++
		}
	}
	t.Logf("%d reviewed regions merge to %d classifications and %d anchors; %d continuation(s) verified",
		len(present), len(merged), anchors, len(continuations))
}
