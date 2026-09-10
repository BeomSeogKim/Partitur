package protectedpath

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

type mutationCopyHelperSite struct {
	file string
	name string
}

func TestMutationCopyHelpersExcludePartiturStateDirectory(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	expected := []mutationCopyHelperSite{
		{"cmd/partitur/draft_result_boundary_mutation_test.go", "copyDraftResultMutationRepository"},
		{"cmd/partitur/prepare_quiesce_mutation_test.go", "copyPrepareQuiesceRepository"},
		{"cmd/partitur/resume_hint_mutation_test.go", "copyDecisionResumeHintMutationRepository"},
		{"integration/composition/mutation_test.go", "copyCompositionMutationRepository"},
		{"internal/adapter/mutation_test.go", "copyAdapterMutationRepository"},
		{"internal/amendment/mutation_test.go", "copyRepository"},
		{"internal/amendmentexec/mutation_test.go", "copyMutationRepository"},
		{"internal/criterionexec/mutation_test.go", "copyCriterionExecMutationRepository"},
		{"internal/driver/mutation_test.go", "copyMutationRepository"},
		{"internal/executiondep/mutation_test.go", "copyRepository"},
		{"internal/faultpoint/mutation_test.go", "copyFaultpointMutationRepository"},
		{"internal/launch/mutation_test.go", "copyLaunchMutationRepository"},
		{"internal/protectedpath/mutation_test.go", "copyRepository"},
		{"internal/recovery/planner_acceptance_worktree_mutation_test.go", "copyRecoveryMutationRepository"},
		{"internal/recoveryconsequence/mutation_test.go", "copyMutationRepository"},
		{"internal/recoveryexec/mutation_test.go", "copyMutationRepository"},
		{"internal/runstate/amendment_norms_mutation_test.go", "copyRunstateMutationRepositoryFrom"},
		{"internal/runstore/mutation_test.go", "copyRunstoreMutationRepository"},
		{"internal/status/mutation_test.go", "copyStatusMutationRepository"},
	}

	// These root walkers are deliberately outside the mutation-copy-helper denominator.
	excluded := map[mutationCopyHelperSite]string{
		{"cmd/partitur/cross_edge_semantic_recovery_faultprobe_test.go", "copyRecoveryTree"}:                            "copies .partitur attempt worktrees intentionally before git worktree repair",
		{"internal/docmarker/remaining_decomposition_mutation_test.go", "copyRemainingDecompositionMutationRepository"}: "copies only the named docs and internal subtrees, never the repository root",
		{"cmd/partitur/e2e_test.go", "repositoryTree"}:                                                                  "walks a temporary test root rather than the repository",
		{"cmd/partitur/init_test.go", "snapshotInitTree"}:                                                               "walks a temporary test root rather than the repository",
		{"internal/recovery/unit_deferral_test.go", "unitOwnedDeferralBoundary"}:                                        "is a source denominator walker rather than a copy helper",
		{"internal/runstate/recovery_coverage_test.go", "hasNonTestAppendSite"}:                                         "is a source denominator walker rather than a copy helper",
	}

	fileSet := token.NewFileSet()
	functions := make(map[mutationCopyHelperSite]string)
	var candidates []mutationCopyHelperSite
	err := filepath.WalkDir(repository, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(repository, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if relative == ".git" || relative == ".partitur" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		parsed, err := parser.ParseFile(fileSet, path, contents, 0)
		if err != nil {
			return err
		}
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			start := fileSet.Position(function.Pos()).Offset
			end := fileSet.Position(function.End()).Offset
			site := mutationCopyHelperSite{filepath.ToSlash(relative), function.Name.Name}
			body := string(contents[start:end])
			functions[site] = body
			if strings.HasPrefix(function.Name.Name, "copy") && strings.Contains(body, "filepath.WalkDir") {
				if _, deliberatelyExcluded := excluded[site]; !deliberatelyExcluded {
					candidates = append(candidates, site)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	sortMutationCopyHelperSites(expected)
	sortMutationCopyHelperSites(candidates)
	if !reflect.DeepEqual(candidates, expected) {
		t.Fatalf("root-walking mutation copy helper inventory = %#v, want %#v", candidates, expected)
	}
	for _, site := range expected {
		if !strings.Contains(functions[site], `".partitur"`) {
			t.Errorf("%s:%s does not exclude .partitur", site.file, site.name)
		}
	}
	for site, reason := range excluded {
		if _, ok := functions[site]; !ok {
			t.Errorf("pinned exclusion %s:%s is missing (%s)", site.file, site.name, reason)
		}
	}
}

func sortMutationCopyHelperSites(sites []mutationCopyHelperSite) {
	sort.Slice(sites, func(i, j int) bool {
		if sites[i].file == sites[j].file {
			return sites[i].name < sites[j].name
		}
		return sites[i].file < sites[j].file
	})
}
