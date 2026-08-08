package main

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/score"
)

const requiredInitIgnore = "runs/\nwork/\n"

const initTestCommandEnvironment = "PARTITUR_INIT_TEST_COMMAND"

func TestInitCommandDispatchIsRegistered(t *testing.T) {
	// Given
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	// When
	found := false
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "runWithReaders" {
			continue
		}
		for _, statement := range function.Body.List {
			conditional, ok := statement.(*ast.IfStmt)
			if !ok {
				continue
			}
			var condition bytes.Buffer
			if err := format.Node(&condition, fileSet, conditional.Cond); err != nil {
				t.Fatal(err)
			}
			callsInitializer := false
			ast.Inspect(conditional.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				identifier, ok := call.Fun.(*ast.Ident)
				if ok && identifier.Name == "initializeRepository" {
					callsInitializer = true
				}
				return true
			})
			if condition.String() == `len(args) == 1 && args[0] == "init"` && callsInitializer {
				found = true
			}
		}
	}

	// Then
	if !found {
		t.Fatal("runWithReaders does not dispatch the one-word init command to initializeRepository")
	}
}

func TestInitCreatesStateAndScoreWhenBothAreAbsent_INIT001(t *testing.T) {
	requireInitSuccess(t, initFixture{})
}

func TestInitPreservesScoreWhenStateIsAbsent_INIT002(t *testing.T) {
	requireInitSuccess(t, initFixture{score: []byte("existing score\n")})
}

func TestInitCreatesIgnoreAndScoreWhenStateExists_INIT003(t *testing.T) {
	requireInitSuccess(t, initFixture{stateExists: true})
}

func TestInitCreatesIgnoreAndPreservesScoreWhenStateExists_INIT004(t *testing.T) {
	requireInitSuccess(t, initFixture{stateExists: true, score: []byte("existing score\n")})
}

func TestInitCreatesScoreWhenIgnoreIsCorrect_INIT005(t *testing.T) {
	requireInitSuccess(t, initFixture{stateExists: true, ignore: []byte(requiredInitIgnore)})
}

func TestInitPreservesCorrectIgnoreAndScore_INIT006(t *testing.T) {
	requireInitSuccess(t, initFixture{
		stateExists: true,
		ignore:      []byte(requiredInitIgnore),
		score:       []byte("existing score\n"),
	})
}

func TestInitRefusesDifferingIgnoreWithoutCreatingScore_INIT007(t *testing.T) {
	requireInitRefusal(t, initFixture{stateExists: true, ignore: []byte("work/\nruns/\n")})
}

func TestInitRefusesDifferingIgnoreWithoutChangingScore_INIT008(t *testing.T) {
	requireInitRefusal(t, initFixture{
		stateExists: true,
		ignore:      []byte("work/\nruns/\n"),
		score:       []byte("existing score\n"),
	})
}

type initFixture struct {
	stateExists bool
	ignore      []byte
	score       []byte
}

func requireInitSuccess(t *testing.T, fixture initFixture) {
	t.Helper()
	repository := writeInitFixture(t, fixture)

	// When
	if code := runInitAt(t, repository); code != 0 {
		t.Fatalf("first init exit=%d, want 0", code)
	}

	// Then
	ignorePath := filepath.Join(repository, ".partitur", ".gitignore")
	ignore, err := os.ReadFile(ignorePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ignore, []byte(requiredInitIgnore)) {
		t.Fatalf("ignore bytes=%q, want %q", ignore, requiredInitIgnore)
	}
	scorePath := filepath.Join(repository, "partitur.yaml")
	scoreBytes, err := os.ReadFile(scorePath)
	if err != nil {
		t.Fatal(err)
	}
	if fixture.score != nil {
		if !bytes.Equal(scoreBytes, fixture.score) {
			t.Fatalf("score bytes=%q, want preserved %q", scoreBytes, fixture.score)
		}
	} else if _, diagnostics := score.Compile(scoreBytes); len(diagnostics) != 0 {
		t.Fatalf("created score does not compile: %v", diagnostics)
	}

	// When
	if code := runInitAt(t, repository); code != 0 {
		t.Fatalf("second init exit=%d, want 0", code)
	}

	// Then
	ignoreAfter, err := os.ReadFile(ignorePath)
	if err != nil {
		t.Fatal(err)
	}
	scoreAfter, err := os.ReadFile(scorePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ignoreAfter, ignore) || !bytes.Equal(scoreAfter, scoreBytes) {
		t.Fatalf("second init changed files: ignore %q -> %q, score %q -> %q", ignore, ignoreAfter, scoreBytes, scoreAfter)
	}
}

func requireInitRefusal(t *testing.T, fixture initFixture) {
	t.Helper()
	repository := writeInitFixture(t, fixture)
	before := snapshotInitTree(t, repository)

	// When
	if code := runInitAt(t, repository); code != 2 {
		t.Fatalf("init exit=%d, want 2", code)
	}

	// Then
	after := snapshotInitTree(t, repository)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("refusal changed filesystem:\n before=%#v\n after=%#v", before, after)
	}
}

func writeInitFixture(t *testing.T, fixture initFixture) string {
	t.Helper()
	repository := t.TempDir()
	if fixture.stateExists {
		if err := os.Mkdir(filepath.Join(repository, ".partitur"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if fixture.ignore != nil {
		if err := os.WriteFile(filepath.Join(repository, ".partitur", ".gitignore"), fixture.ignore, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if fixture.score != nil {
		if err := os.WriteFile(filepath.Join(repository, "partitur.yaml"), fixture.score, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return repository
}

func runInitAt(t *testing.T, repository string) int {
	t.Helper()
	command := exec.Command(os.Args[0], "init")
	command.Dir = repository
	command.Env = append(os.Environ(), initTestCommandEnvironment+"=1")
	if err := command.Run(); err != nil {
		exitError, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		return exitError.ExitCode()
	}
	return 0
}

func snapshotInitTree(t *testing.T, root string) map[string][]byte {
	t.Helper()
	snapshot := make(map[string][]byte)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			snapshot[relative+"/"] = nil
			return nil
		}
		bytes, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		snapshot[relative] = bytes
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
