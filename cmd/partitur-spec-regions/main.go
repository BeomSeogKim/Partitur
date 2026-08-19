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
	packet := flag.Int("packet", 0, "display and propose for one packet")
	context := flag.Int("context", 2, "surrounding physical lines to display")
	registryPath := flag.String("registry", "docs/DESIGN.clause-staging.json", "staging receipt registry")
	materializePath := flag.String("materialize", "", "write confirmed classifications to this new document")
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
	if err := docclause.ValidateRegistry(document, blob, regions, registry); err != nil {
		fail(err)
	}
	for _, region := range regions {
		fmt.Printf("packet %02d lines %d-%d: catalog-id=%d none=%d\n", region.Key.Ordinal, region.Key.StartLine, region.Key.EndLine, region.CatalogIDLines, region.WithoutCatalogID)
	}
	pending := docclause.Pending(regions, registry)
	fmt.Printf("pending=%d/%d\n", len(pending), len(regions))
	if *materializePath != "" {
		if *materializePath == *documentPath {
			fail(fmt.Errorf("materialization output must not overwrite source document"))
		}
		materialized, err := docclause.Materialize(document, blob, regions, registry)
		if err != nil {
			fail(err)
		}
		if err := os.WriteFile(*materializePath, materialized, 0o600); err != nil {
			fail(err)
		}
		digest, err := docclause.ClassificationDigest(document, blob, regions, registry)
		if err != nil {
			fail(err)
		}
		fmt.Printf("classification-digest=%s\n", digest)
	}
	if *packet == 0 {
		return
	}
	rendered, err := docclause.RenderRegion(regions, *packet, *context)
	if err != nil {
		fail(err)
	}
	fmt.Print(rendered)
	proposal := docclause.Propose(regions[*packet-1])
	encoded, err := json.MarshalIndent(proposal, "", "  ")
	if err != nil {
		fail(err)
	}
	fmt.Println(string(encoded))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
