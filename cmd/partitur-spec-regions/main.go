package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/BeomSeogKim/Partitur/internal/docclause"
)

func main() {
	documentPath := flag.String("document", "docs/DESIGN.md", "unmarked specification document")
	packet := flag.Int("packet", 0, "display one review packet without proposing classifications")
	previewPacket := flag.Int("preview-packet", 0, "render one packet with confirmed classifications inline as a view on stdout")
	context := flag.Int("context", 2, "surrounding physical lines to display")
	registryPath := flag.String("registry", "docs/DESIGN.clause-staging.json", "staging receipt registry")
	materializePath := flag.String("materialize", "", "write confirmed classifications to this new document")
	markedPath := flag.String("marked", "", "materialized document to validate against activation pins")
	checkActivation := flag.Bool("check-activation", false, "require all four atomic baseline activation conditions")
	flag.Parse()

	document, err := os.ReadFile(*documentPath)
	if err != nil {
		fail(err)
	}
	command := exec.Command("git", "hash-object", "--stdin")
	command.Stdin = strings.NewReader(string(document))
	output, err := command.Output()
	if err != nil {
		fail(err)
	}
	blob := strings.TrimSpace(string(output))
	regions, err := docclause.GenerateRegions(document, blob)
	if err != nil {
		fail(err)
	}
	registryContents, err := os.ReadFile(*registryPath)
	if err != nil {
		fail(err)
	}
	var registry docclause.Registry
	decoder := json.NewDecoder(bytes.NewReader(registryContents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&registry); err != nil {
		fail(err)
	}
	if err := docclause.ValidateRegistry(*documentPath, document, blob, regions, registry); err != nil {
		fail(err)
	}
	if *previewPacket != 0 {
		if *packet != 0 || *materializePath != "" || *markedPath != "" || *checkActivation {
			fail(fmt.Errorf("-preview-packet is a view-only mode and cannot be combined with document or activation modes"))
		}
		preview, err := docclause.RenderPacketPreview(regions, registry, *previewPacket)
		if err != nil {
			fail(err)
		}
		if _, err := preview.WriteTo(os.Stdout); err != nil {
			fail(err)
		}
		return
	}
	pending := docclause.Pending(regions, registry)
	pendingByOrdinal := make(map[int]bool, len(pending))
	for _, key := range pending {
		pendingByOrdinal[key.Ordinal] = true
	}
	for _, region := range regions {
		fmt.Printf("packet %02d lines %d-%d: reviewed=%t\n", region.Key.Ordinal, region.Key.StartLine, region.Key.EndLine, !pendingByOrdinal[region.Key.Ordinal])
	}
	fmt.Printf("pending=%d/%d\n", len(pending), len(regions))
	if *materializePath != "" {
		if *materializePath == *documentPath {
			fail(fmt.Errorf("materialization output must not overwrite source document"))
		}
		materialized, err := docclause.Materialize(*documentPath, document, blob, regions, registry)
		if err != nil {
			fail(err)
		}
		if err := os.WriteFile(*materializePath, materialized, 0o600); err != nil {
			fail(err)
		}
		digest, err := docclause.ClassificationDigest(*documentPath, document, blob, regions, registry)
		if err != nil {
			fail(err)
		}
		fmt.Printf("classification-digest=%s\n", digest)
		fmt.Printf("marked-blob=%s\n", docclause.GitBlobID(materialized))
	}
	if *checkActivation {
		if *markedPath == "" {
			fail(fmt.Errorf("-marked is required with -check-activation"))
		}
		marked, err := os.ReadFile(*markedPath)
		if err != nil {
			fail(err)
		}
		if err := docclause.ValidateActivation(*documentPath, document, marked, blob, regions, registry); err != nil {
			fail(err)
		}
		fmt.Println("baseline-activation=valid")
	}
	if *packet == 0 {
		return
	}
	rendered, err := docclause.RenderRegion(regions, *packet, *context)
	if err != nil {
		fail(err)
	}
	fmt.Print(rendered)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
