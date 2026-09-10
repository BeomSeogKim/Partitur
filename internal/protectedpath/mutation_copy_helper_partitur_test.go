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
	"strconv"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/mutationtest"
)

type mutationCopyHelperSite struct {
	file string
	name string
}

func TestMutationCopyHelpersExcludePartiturStateDirectory(t *testing.T) {
	t.Run("enumeration", func(t *testing.T) {
		repository := filepath.Clean(filepath.Join("..", ".."))
		expected := []mutationCopyHelperSite{
			{"internal/mutationtest/copy.go", "CopyRepository"},
		}
		// These direct WalkDir callers serve purposes outside repository mutation copies.
		excluded := map[mutationCopyHelperSite]string{
			{"cmd/partitur/cross_edge_semantic_recovery_faultprobe_test.go", "copyRecoveryTree"}:                                     "copies .partitur attempt worktrees intentionally before git worktree repair",
			{"cmd/partitur/e2e_test.go", "repositoryTree"}:                                                                           "walks a temporary test root rather than the repository",
			{"cmd/partitur/init_test.go", "snapshotInitTree"}:                                                                        "walks a temporary test root rather than the repository",
			{"internal/recovery/unit_deferral_test.go", "unitOwnedDeferralBoundary"}:                                                 "is a source denominator walker rather than a copy helper",
			{"internal/runstate/recovery_coverage_test.go", "hasNonTestAppendSite"}:                                                  "is a source denominator walker rather than a copy helper",
			{"internal/protectedpath/mutation_copy_helper_partitur_test.go", "TestMutationCopyHelpersExcludePartiturStateDirectory"}: "enumerates source functions for this guard rather than copying trees",
			{"internal/workspace/attempt.go", "snapshotProtected"}:                                                                   "hashes protected subtrees rather than copying the repository root",
		}
		fileSet := token.NewFileSet()
		walkers := make(map[mutationCopyHelperSite]bool)
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
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			parsed, err := parser.ParseFile(fileSet, path, nil, 0)
			if err != nil {
				return err
			}
			filepathImport := ""
			for _, spec := range parsed.Imports {
				importPath, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					return err
				}
				if importPath == "path/filepath" {
					filepathImport = "filepath"
					if spec.Name != nil {
						filepathImport = spec.Name.Name
					}
				}
			}
			for _, declaration := range parsed.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if !ok || function.Body == nil {
					continue
				}
				// Resolve direct calls through the file's import, including aliases.
				// Indirect calls, other traversal APIs, and recursion are outside this guard.
				hasWalkDir := false
				ast.Inspect(function.Body, func(node ast.Node) bool {
					call, ok := node.(*ast.CallExpr)
					if !ok {
						return true
					}
					switch callee := call.Fun.(type) {
					case *ast.SelectorExpr:
						qualifier, ok := callee.X.(*ast.Ident)
						if ok && qualifier.Obj == nil && qualifier.Name == filepathImport && callee.Sel.Name == "WalkDir" {
							hasWalkDir = true
						}
					case *ast.Ident:
						if filepathImport == "." && callee.Obj == nil && callee.Name == "WalkDir" {
							hasWalkDir = true
						}
					}
					return true
				})
				if hasWalkDir {
					site := mutationCopyHelperSite{filepath.ToSlash(relative), function.Name.Name}
					walkers[site] = true
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
			t.Errorf("root-walking mutation copy helper inventory = %#v, want %#v", candidates, expected)
		}
		for site, reason := range excluded {
			if !walkers[site] {
				t.Errorf("pinned exclusion %s:%s is missing a direct filepath.WalkDir call (%s)", site.file, site.name, reason)
			}
		}
	})

	t.Run("behaviour", func(t *testing.T) {
		for _, gitKind := range []string{"file", "directory"} {
			t.Run("git_"+gitKind, func(t *testing.T) {
				source := t.TempDir()
				destination := filepath.Join(t.TempDir(), "copy")
				write := func(relative, contents string) {
					t.Helper()
					path := filepath.Join(source, relative)
					if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if gitKind == "file" {
					write(".git", "gitdir: elsewhere")
				} else {
					write(".git/config", "repository metadata")
				}
				write(".partitur/cast.yaml", "tracked state")
				write("z-after-git", "sentinel")
				write("nested/.partitur/keep", "nested state")
				if err := os.Symlink("z-after-git", filepath.Join(source, "link")); err != nil {
					t.Fatal(err)
				}
				if err := mutationtest.CopyRepository(destination, source); err != nil {
					t.Fatal(err)
				}
				for _, relative := range []string{".git", ".partitur"} {
					if _, err := os.Lstat(filepath.Join(destination, relative)); !os.IsNotExist(err) {
						t.Errorf("excluded %s exists or cannot be inspected: %v", relative, err)
					}
				}
				for relative, want := range map[string]string{"z-after-git": "sentinel", "nested/.partitur/keep": "nested state"} {
					got, err := os.ReadFile(filepath.Join(destination, relative))
					if err != nil || string(got) != want {
						t.Errorf("copied %s = %q, %v; want %q", relative, got, err, want)
					}
				}
				if target, err := os.Readlink(filepath.Join(destination, "link")); err != nil || target != "z-after-git" {
					t.Errorf("preserved symlink = %q, %v; want z-after-git", target, err)
				}
			})
		}
	})
}

func sortMutationCopyHelperSites(sites []mutationCopyHelperSite) {
	sort.Slice(sites, func(i, j int) bool {
		if sites[i].file == sites[j].file {
			return sites[i].name < sites[j].name
		}
		return sites[i].file < sites[j].file
	})
}
