package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	for _, argument := range os.Args[1:] {
		if argument == "--version" {
			fmt.Println("codex 1.0.0")
			return
		}
	}
	prompt, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(91)
	}
	outputDir := ""
	artifactID := ""
	for _, line := range strings.Split(string(prompt), "\n") {
		if strings.HasPrefix(line, "- Writable artifact directory: ") {
			outputDir = strings.TrimSpace(strings.TrimPrefix(line, "- Writable artifact directory: "))
		}
		if strings.HasPrefix(line, "- artifact_id=\"") {
			artifactID, _, _ = strings.Cut(strings.TrimPrefix(line, "- artifact_id=\""), "\"")
		}
	}
	if outputDir == "" {
		os.Exit(92)
	}
	if strings.Contains(string(prompt), "## Instruction\n\nwriter-") {
		value := "one\n"
		if strings.Contains(string(prompt), "writer-two") {
			value = "two\n"
		}
		if err := os.WriteFile("conflict.txt", []byte(value), 0o600); err != nil {
			os.Exit(93)
		}
	}
	artifacts := []any{}
	if artifactID != "" {
		if err := os.WriteFile(filepath.Join(outputDir, "report.txt"), []byte("report\n"), 0o600); err != nil {
			os.Exit(94)
		}
		artifacts = append(artifacts, map[string]any{"artifact_id": artifactID, "path": "report.txt"})
	}
	data, err := json.Marshal(map[string]any{"version": 1, "artifacts": artifacts, "questions": []any{}, "proposal": nil, "summary": "completed"})
	if err != nil {
		os.Exit(95)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "partitur-result.json"), data, 0o600); err != nil {
		os.Exit(96)
	}
	fmt.Println(`{"type":"fixture.ignored"}`)
}
