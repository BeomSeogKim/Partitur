package docclause

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestRegionUniverseCompleteness(t *testing.T) {
	document := testDocument(401)
	regions, err := GenerateRegions(document, "blob-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(regions) != 3 {
		t.Fatalf("regions = %d, want 3", len(regions))
	}
	if got := [][2]int{{regions[0].Key.StartLine, regions[0].Key.EndLine}, {regions[1].Key.StartLine, regions[1].Key.EndLine}, {regions[2].Key.StartLine, regions[2].Key.EndLine}}; got[0] != [2]int{1, 219} || got[1] != [2]int{220, 439} || got[2] != [2]int{440, 441} {
		t.Fatalf("region ranges = %v, want [[1 219] [220 439] [440 441]]", got)
	}

	mutations := []struct {
		name   string
		mutate func([]Region)
		want   string
	}{
		{"different blob", func(got []Region) { got[1].Key.InputBlob = "blob-b" }, "does not match"},
		{"gap", func(got []Region) { got[1].Key.StartLine++; got[1].Lines = got[1].Lines[1:] }, "gap or overlap"},
		{"overlap", func(got []Region) { got[0].Key.EndLine++; got[0].Lines = append(got[0].Lines, regions[1].Lines[0]) }, "gap or overlap"},
		{"duplicate ordinal", func(got []Region) { got[1].Key.Ordinal = 1 }, "ordinal"},
		{"changed source bytes", func(got []Region) { got[0].Lines[0] = "changed" }, "source bytes"},
		{"truncated universe", func(got []Region) { got[1].Key.EndLine--; got[1].Lines = got[1].Lines[:len(got[1].Lines)-1] }, "nonblank lines"},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			got := cloneRegions(regions)
			mutation.mutate(got)
			err := ValidateUniverse(document, "blob-a", got)
			if err == nil || !strings.Contains(err.Error(), mutation.want) {
				t.Fatalf("validation error = %v, want %q", err, mutation.want)
			}
		})
	}
}

func TestRegistryStructuralChecksAndReceiptInvalidation(t *testing.T) {
	document := []byte("alpha\nbeta\ngamma\n")
	regions, err := GenerateRegions(document, "blob-a")
	if err != nil {
		t.Fatal(err)
	}
	valid := Registry{Receipts: []RegionReceipt{{
		Key: regions[0].Key,
		Review: &ReviewReceipt{SourceSHA256: SourceDigest(regions[0].Lines), Decisions: []Classification{
			{StartLine: 1, EndLine: 1, Kind: ClassificationAnchor, MarkerID: "proposed/alpha"},
			{StartLine: 2, EndLine: 3, Kind: ClassificationNonNormative},
		}},
	}}}
	if err := ValidateRegistry(document, "blob-a", regions, valid); err != nil {
		t.Fatalf("valid registry rejected: %v", err)
	}
	if pending := Pending(regions, valid); len(pending) != 0 {
		t.Fatalf("valid registry pending = %v", pending)
	}

	tests := []struct {
		name   string
		mutate func(*Registry)
		want   string
	}{
		{"duplicate receipt", func(got *Registry) { got.Receipts = append(got.Receipts, got.Receipts[0]) }, "duplicate region receipt"},
		{"registry mismatch", func(got *Registry) { got.Receipts[0].Key.EndLine++ }, "does not match immutable universe"},
		{"decision gap", func(got *Registry) { got.Receipts[0].Review.Decisions[1].StartLine++ }, "gap or overlap"},
		{"decision overlap", func(got *Registry) { got.Receipts[0].Review.Decisions[1].StartLine-- }, "gap or overlap"},
		{"invalid marker", func(got *Registry) { got.Receipts[0].Review.Decisions[0].MarkerID = "not valid" }, "invalid marker ID"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := cloneRegistry(valid)
			test.mutate(&got)
			err := ValidateRegistry(document, "blob-a", regions, got)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v, want %q", err, test.want)
			}
		})
	}

	stale := cloneRegistry(valid)
	stale.Receipts[0].Review.SourceSHA256 = strings.Repeat("0", 64)
	if pending := Pending(regions, stale); len(pending) != 1 {
		t.Fatalf("stale receipt pending = %v, want sole region", pending)
	}
}

func TestPendingDigestAndMaterialization(t *testing.T) {
	document := []byte("alpha\nbeta\ngamma\n")
	regions, err := GenerateRegions(document, "blob-a")
	if err != nil {
		t.Fatal(err)
	}
	if pending := Pending(regions, StagingRegistry); len(pending) != 1 {
		t.Fatalf("empty staging pending = %v, want sole region", pending)
	}
	if _, err := ClassificationDigest(document, "blob-a", regions, StagingRegistry); err == nil || !strings.Contains(err.Error(), "pending") {
		t.Fatalf("empty staging digest error = %v, want pending", err)
	}
	registry := Registry{Receipts: []RegionReceipt{{
		Key: regions[0].Key,
		Review: &ReviewReceipt{SourceSHA256: SourceDigest(regions[0].Lines), Decisions: []Classification{
			{StartLine: 1, EndLine: 2, Kind: ClassificationAnchor, MarkerID: "example.alpha"},
			{StartLine: 3, EndLine: 3, Kind: ClassificationNonNormative},
		}},
	}}}
	digest, err := ClassificationDigest(document, "blob-a", regions, registry)
	if err != nil {
		t.Fatal(err)
	}
	if len(digest) != 64 {
		t.Fatalf("digest length = %d, want 64", len(digest))
	}
	materialized, err := Materialize(document, "blob-a", regions, registry)
	if err != nil {
		t.Fatal(err)
	}
	want := "<!-- partitur:mark begin anchor=example.alpha -->\nalpha\nbeta\n<!-- partitur:mark end anchor=example.alpha -->\n<!-- partitur:mark begin non-normative -->\ngamma\n<!-- partitur:mark end non-normative -->\n"
	if string(materialized) != want {
		t.Fatalf("materialized =\n%s\nwant:\n%s", materialized, want)
	}
	tampered := bytes.Replace(materialized, []byte("anchor=example.alpha"), []byte("anchor=example.beta"), 2)
	if err := ValidateMaterialized(tampered, document, "blob-a", regions, registry); err == nil || !strings.Contains(err.Error(), "does not match registry") {
		t.Fatalf("materialized registry mismatch error = %v", err)
	}
	unmatched := []byte("<!-- partitur:mark begin anchor=broken -->\nalpha\n")
	if _, err := Materialize(unmatched, "blob-a", regions, registry); err == nil || !strings.Contains(err.Error(), "unmatched source marker token") {
		t.Fatalf("unmatched marker error = %v", err)
	}

	duplicate := cloneRegistry(registry)
	duplicate.Receipts[0].Review.Decisions = []Classification{
		{StartLine: 1, EndLine: 1, Kind: ClassificationAnchor, MarkerID: "duplicate"},
		{StartLine: 2, EndLine: 2, Kind: ClassificationNonNormative},
		{StartLine: 3, EndLine: 3, Kind: ClassificationAnchor, MarkerID: "duplicate"},
	}
	if _, err := ClassificationDigest(document, "blob-a", regions, duplicate); err == nil || !strings.Contains(err.Error(), "duplicate anchor") {
		t.Fatalf("duplicate anchor error = %v", err)
	}
}

func TestProposalCarriesNoClassificationJudgement(t *testing.T) {
	document := []byte("# Heading\n\nRC-RESUME-001 applies here.\ncontinued\n")
	regions, err := GenerateRegions(document, "blob-a")
	if err != nil {
		t.Fatal(err)
	}
	proposal := Propose(regions[0])
	if len(proposal.Anchors) != 1 || proposal.Anchors[0].MarkerID != "RC-RESUME-001" {
		t.Fatalf("anchor proposals = %+v", proposal.Anchors)
	}
	if len(proposal.Boundaries) != 2 || len(proposal.Names) != 1 {
		t.Fatalf("proposal = %+v", proposal)
	}
	encoded, err := json.Marshal(proposal)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), string(ClassificationNonNormative)) || strings.Contains(string(encoded), `"kind"`) {
		t.Fatal("proposal classified content as non-normative")
	}
}

func testDocument(nonblank int) []byte {
	var document strings.Builder
	for index := 1; index <= nonblank; index++ {
		document.WriteString("line\n")
		if index%10 == 0 {
			document.WriteByte('\n')
		}
	}
	return []byte(document.String())
}

func cloneRegions(source []Region) []Region {
	clone := append([]Region(nil), source...)
	for index := range clone {
		clone[index].Lines = append([]string(nil), source[index].Lines...)
	}
	return clone
}

func cloneRegistry(source Registry) Registry {
	clone := Registry{Receipts: append([]RegionReceipt(nil), source.Receipts...)}
	for index := range clone.Receipts {
		if source.Receipts[index].Review != nil {
			review := *source.Receipts[index].Review
			review.Decisions = append([]Classification(nil), review.Decisions...)
			clone.Receipts[index].Review = &review
		}
	}
	return clone
}
