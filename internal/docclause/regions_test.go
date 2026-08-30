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
	document := []byte("alpha beta\n")
	blob := GitBlobID(document)
	regions, err := GenerateRegions(document, blob)
	if err != nil {
		t.Fatal(err)
	}
	valid := testRegistry("docs/example.md", blob, regions, []RegionReceipt{{
		Key: regions[0].Key,
		Review: &ReviewReceipt{SourceSHA256: SourceDigest(regions[0].Lines), Decisions: []Classification{
			{StartByte: 0, EndByte: 5, Kind: ClassificationAnchor, MarkerID: "proposed/alpha"},
			{StartByte: 6, EndByte: 10, Kind: ClassificationNonNormative},
		}},
	}})
	if err := ValidateRegistry("docs/example.md", document, blob, regions, valid); err != nil {
		t.Fatalf("valid registry rejected: %v", err)
	}
	changedDocument := bytes.Replace(document, []byte("alpha"), []byte("ALPHA"), 1)
	if err := ValidateRegistry("docs/example.md", changedDocument, blob, regions, valid); err == nil || !strings.Contains(err.Error(), "document blob") {
		t.Fatalf("changed document error = %v, want document blob mismatch", err)
	}
	encodedDecision, err := json.Marshal(valid.Receipts[0].Review.Decisions[0])
	if err != nil {
		t.Fatal(err)
	}
	if got := string(encodedDecision); !strings.Contains(got, `"start_source_byte":0`) || !strings.Contains(got, `"end_source_byte":5`) || strings.Contains(got, "source_line") {
		t.Fatalf("classification receipt coordinates = %s, want byte offsets only", got)
	}
	if pending := Pending(regions, valid); len(pending) != 0 {
		t.Fatalf("valid registry pending = %v", pending)
	}

	tests := []struct {
		name   string
		mutate func(*Registry)
		want   string
	}{
		{"document path", func(got *Registry) { got.DocumentPath = "docs/other.md" }, "document path"},
		{"input blob", func(got *Registry) { got.InputBlob = strings.Repeat("0", 40) }, "registry input blob"},
		{"workload parameter", func(got *Registry) { got.NonblankLinesPerRegion++ }, "nonblank lines per region"},
		{"region universe key", func(got *Registry) { got.RegionUniverse[0].EndLine++ }, "universe key"},
		{"duplicate receipt", func(got *Registry) { got.Receipts = append(got.Receipts, got.Receipts[0]) }, "duplicate region receipt"},
		{"registry mismatch", func(got *Registry) { got.Receipts[0].Key.EndLine++ }, "does not match immutable universe"},
		{"within_line_overlap", func(got *Registry) { got.Receipts[0].Review.Decisions[1].StartByte = 4 }, "overlap"},
		{"invalid marker", func(got *Registry) { got.Receipts[0].Review.Decisions[0].MarkerID = "not valid" }, "invalid marker ID"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := cloneRegistry(valid)
			test.mutate(&got)
			err := ValidateRegistry("docs/example.md", document, blob, regions, got)
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

func TestActivationRequiresCompleteMaterializedAndPinnedClassification(t *testing.T) {
	document := []byte("alpha beta\n")
	blob := GitBlobID(document)
	regions, err := GenerateRegions(document, blob)
	if err != nil {
		t.Fatal(err)
	}
	registry := testRegistry("docs/example.md", blob, regions, []RegionReceipt{{
		Key: regions[0].Key,
		Review: &ReviewReceipt{SourceSHA256: SourceDigest(regions[0].Lines), Decisions: []Classification{
			{StartByte: 0, EndByte: 5, Kind: ClassificationAnchor, MarkerID: "example.alpha"},
			{StartByte: 6, EndByte: 10, Kind: ClassificationNonNormative},
		}},
	}})
	if pending := Pending(regions, registry); len(pending) != 0 {
		t.Fatalf("complete synthetic registry pending = %v, want empty", pending)
	}
	marked, err := Materialize("docs/example.md", document, blob, regions, registry)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := ClassificationDigest("docs/example.md", document, blob, regions, registry)
	if err != nil {
		t.Fatal(err)
	}
	registry.Activation = &ActivationPins{
		MarkedBlob:                  GitBlobID(marked),
		OrderedClassificationSHA256: digest,
	}
	if err := ValidateActivation("docs/example.md", document, marked, blob, regions, registry); err != nil {
		t.Fatalf("valid activation rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Registry, *[]byte)
		want   string
	}{
		{"missing activation", func(got *Registry, _ *[]byte) { got.Activation = nil }, "activation is absent"},
		{"materialized bytes", func(_ *Registry, got *[]byte) { *got = bytes.Replace(*got, []byte("alpha"), []byte("ALPHA"), 1) }, "does not equal materialized"},
		{"marked blob pin", func(got *Registry, _ *[]byte) { got.Activation.MarkedBlob = strings.Repeat("0", 40) }, "marked blob"},
		{"classification pin", func(got *Registry, _ *[]byte) { got.Activation.OrderedClassificationSHA256 = strings.Repeat("0", 64) }, "ordered classification digest"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotRegistry := cloneRegistry(registry)
			activation := *registry.Activation
			gotRegistry.Activation = &activation
			gotMarked := append([]byte(nil), marked...)
			test.mutate(&gotRegistry, &gotMarked)
			err := ValidateActivation("docs/example.md", document, gotMarked, blob, regions, gotRegistry)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("activation error = %v, want %q", err, test.want)
			}
		})
	}
	t.Run("packet_preview", func(t *testing.T) {
		preview, err := RenderPacketPreview(regions, registry, 1)
		if err != nil {
			t.Fatal(err)
		}
		var rendered bytes.Buffer
		if _, err := preview.WriteTo(&rendered); err != nil {
			t.Fatal(err)
		}
		err = ValidateActivation("docs/example.md", document, rendered.Bytes(), blob, regions, registry)
		if err == nil || !strings.Contains(err.Error(), "does not equal materialized") {
			t.Fatalf("preview substitution error = %v, want materialized equality refusal", err)
		}
	})
}

func TestPendingDigestAndMaterialization(t *testing.T) {
	document := []byte("alpha beta\n")
	blob := GitBlobID(document)
	regions, err := GenerateRegions(document, blob)
	if err != nil {
		t.Fatal(err)
	}
	empty := testRegistry("docs/example.md", blob, regions, nil)
	if pending := Pending(regions, empty); len(pending) != 1 {
		t.Fatalf("empty staging pending = %v, want sole region", pending)
	}
	if _, err := ClassificationDigest("docs/example.md", document, blob, regions, empty); err == nil || !strings.Contains(err.Error(), "pending") {
		t.Fatalf("empty staging digest error = %v, want pending", err)
	}
	registry := testRegistry("docs/example.md", blob, regions, []RegionReceipt{{
		Key: regions[0].Key,
		Review: &ReviewReceipt{SourceSHA256: SourceDigest(regions[0].Lines), Decisions: []Classification{
			{StartByte: 0, EndByte: 5, Kind: ClassificationAnchor, MarkerID: "example.alpha"},
			{StartByte: 6, EndByte: 10, Kind: ClassificationNonNormative},
		}},
	}})
	digest, err := ClassificationDigest("docs/example.md", document, blob, regions, registry)
	if err != nil {
		t.Fatal(err)
	}
	if len(digest) != 64 {
		t.Fatalf("digest length = %d, want 64", len(digest))
	}
	materialized, err := Materialize("docs/example.md", document, blob, regions, registry)
	if err != nil {
		t.Fatal(err)
	}
	want := "<!-- partitur:mark begin anchor=example.alpha -->alpha<!-- partitur:mark end anchor=example.alpha --> <!-- partitur:mark begin non-normative -->beta<!-- partitur:mark end non-normative -->\n"
	if string(materialized) != want {
		t.Fatalf("materialized =\n%s\nwant:\n%s", materialized, want)
	}
	tampered := bytes.Replace(materialized, []byte("anchor=example.alpha"), []byte("anchor=example.beta"), 2)
	if err := ValidateMaterialized("docs/example.md", tampered, document, blob, regions, registry); err == nil || !strings.Contains(err.Error(), "does not match registry") {
		t.Fatalf("materialized registry mismatch error = %v", err)
	}
	unmatched := []byte("<!-- partitur:mark begin anchor=broken -->\nalpha\n")
	if _, err := Materialize("docs/example.md", unmatched, blob, regions, registry); err == nil || !strings.Contains(err.Error(), "unmatched source marker token") {
		t.Fatalf("unmatched marker error = %v", err)
	}

	duplicate := cloneRegistry(registry)
	duplicate.Receipts[0].Review.Decisions = []Classification{
		{StartByte: 0, EndByte: 5, Kind: ClassificationAnchor, MarkerID: "duplicate"},
		{StartByte: 6, EndByte: 10, Kind: ClassificationAnchor, MarkerID: "duplicate"},
	}
	if _, err := ClassificationDigest("docs/example.md", document, blob, regions, duplicate); err == nil || !strings.Contains(err.Error(), "duplicate anchor") {
		t.Fatalf("duplicate anchor error = %v", err)
	}
}

func TestPendingPacketPreviewRendersOnlyConfirmedClassifications(t *testing.T) {
	document := []byte("alpha beta gamma\n")
	blob := GitBlobID(document)
	regions, err := GenerateRegions(document, blob)
	if err != nil {
		t.Fatal(err)
	}
	registry := testRegistry("docs/example.md", blob, regions, []RegionReceipt{{
		Key: regions[0].Key,
		Review: &ReviewReceipt{SourceSHA256: SourceDigest(regions[0].Lines), Decisions: []Classification{
			{StartByte: 0, EndByte: 5, Kind: ClassificationAnchor, MarkerID: "example.alpha"},
			{StartByte: 11, EndByte: 16, Kind: ClassificationNonNormative},
		}},
	}})
	if err := ValidateRegistry("docs/example.md", document, blob, regions, registry); err != nil {
		t.Fatalf("partial confirmed registry rejected: %v", err)
	}
	if pending := Pending(regions, registry); len(pending) != 1 {
		t.Fatalf("partial confirmed registry pending = %v, want sole region", pending)
	}
	if _, err := ClassificationDigest("docs/example.md", document, blob, regions, registry); err == nil || !strings.Contains(err.Error(), "pending") {
		t.Fatalf("partial confirmed digest error = %v, want pending", err)
	}

	preview, err := RenderPacketPreview(regions, registry, 1)
	if err != nil {
		t.Fatal(err)
	}
	var rendered bytes.Buffer
	if _, err := preview.WriteTo(&rendered); err != nil {
		t.Fatal(err)
	}
	want := "PARTITUR PACKET PREVIEW — VIEW ONLY; NOT AN ACTIVATION DOCUMENT\n" +
		"packet 01 lines 1-1\n" +
		"legend: [[CONFIRMED ...]]...[[/CONFIRMED]]  [[UNCLASSIFIED]]...[[/UNCLASSIFIED]]\n\n" +
		"[[CONFIRMED anchor=example.alpha]]alpha[[/CONFIRMED]] [[UNCLASSIFIED]]beta[[/UNCLASSIFIED]] [[CONFIRMED non-normative]]gamma[[/CONFIRMED]]\n"
	if got := rendered.String(); got != want {
		t.Fatalf("packet preview =\n%s\nwant:\n%s", got, want)
	}
	if strings.Contains(rendered.String(), "partitur:mark") {
		t.Fatal("packet preview contains activation marker syntax")
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
	clone := source
	if source.Activation != nil {
		activation := *source.Activation
		clone.Activation = &activation
	}
	clone.RegionUniverse = append([]RegionKey(nil), source.RegionUniverse...)
	clone.Receipts = append([]RegionReceipt(nil), source.Receipts...)
	for index := range clone.Receipts {
		if source.Receipts[index].Review != nil {
			review := *source.Receipts[index].Review
			review.Decisions = append([]Classification(nil), review.Decisions...)
			clone.Receipts[index].Review = &review
		}
	}
	return clone
}

func testRegistry(documentPath, inputBlob string, regions []Region, receipts []RegionReceipt) Registry {
	universe := make([]RegionKey, len(regions))
	for index, region := range regions {
		universe[index] = region.Key
	}
	return Registry{
		DocumentPath:           documentPath,
		InputBlob:              inputBlob,
		NonblankLinesPerRegion: NonblankLinesPerRegion,
		RegionUniverse:         universe,
		Receipts:               receipts,
	}
}
