package fanin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

type entry struct {
	mode string
	data []byte
	oid  string
}

type changeSet struct {
	id     string
	base   string
	result string
}

type mergeResult struct {
	Clean bool     `json:"clean"`
	Tree  string   `json:"tree"`
	Paths []string `json:"paths,omitempty"`
}

type repository struct {
	t        *testing.T
	gitDir   string
	workTree string
}

func TestFanInMatrix(t *testing.T) {
	repo := newRepository(t)
	version := strings.TrimSpace(repo.git("--version"))
	results := map[string]mergeResult{}

	t.Run("rename_rename", func(t *testing.T) {
		base := repo.tree(map[string]entry{"old.txt": file("base\n")})
		ours := repo.tree(map[string]entry{"left.txt": file("base\n")})
		theirs := repo.tree(map[string]entry{"right.txt": file("base\n")})
		results["rename_rename"] = repo.merge(base, ours, theirs)
		requireConflictContains(t, results["rename_rename"], "old.txt", "left.txt", "right.txt")
	})

	t.Run("rename_delete", func(t *testing.T) {
		base := repo.tree(map[string]entry{"old.txt": file("base\n")})
		ours := repo.tree(map[string]entry{"new.txt": file("base\n")})
		theirs := repo.tree(nil)
		results["rename_delete"] = repo.merge(base, ours, theirs)
		requireConflict(t, results["rename_delete"], "new.txt")
	})

	t.Run("add_add", func(t *testing.T) {
		base := repo.tree(nil)
		ours := repo.tree(map[string]entry{"same.txt": file("ours\n")})
		theirs := repo.tree(map[string]entry{"same.txt": file("theirs\n")})
		results["add_add"] = repo.merge(base, ours, theirs)
		requireConflict(t, results["add_add"], "same.txt")
	})

	t.Run("conflict_path_with_newline", func(t *testing.T) {
		const path = "line\nbreak"
		base := repo.tree(map[string]entry{path: file("base\n")})
		ours := repo.tree(map[string]entry{path: file("ours\n")})
		theirs := repo.tree(map[string]entry{path: file("theirs\n")})
		results["conflict_path_with_newline"] = repo.merge(base, ours, theirs)
		requireConflict(t, results["conflict_path_with_newline"], path)
	})

	t.Run("mode_and_content", func(t *testing.T) {
		base := repo.tree(map[string]entry{"script": file("#!/bin/sh\necho base\n")})
		executable := file("#!/bin/sh\necho base\n")
		executable.mode = "100755"
		ours := repo.tree(map[string]entry{"script": executable})
		theirs := repo.tree(map[string]entry{"script": file("#!/bin/sh\necho changed\n")})
		result := repo.merge(base, ours, theirs)
		requireClean(t, result)
		mode, data := repo.readPath(result.Tree, "script")
		if mode != "100755" || string(data) != "#!/bin/sh\necho changed\n" {
			t.Fatalf("mode=%s data=%q", mode, data)
		}
		results["mode_and_content"] = result
	})

	t.Run("symlink_target", func(t *testing.T) {
		base := repo.tree(map[string]entry{"link": symlink("base")})
		ours := repo.tree(map[string]entry{"link": symlink("ours")})
		theirs := repo.tree(map[string]entry{"link": symlink("theirs")})
		results["symlink_target"] = repo.merge(base, ours, theirs)
		requireConflict(t, results["symlink_target"], "link")
	})

	t.Run("symlink_to_file", func(t *testing.T) {
		base := repo.tree(map[string]entry{"node": symlink("base")})
		ours := repo.tree(map[string]entry{"node": file("regular\n")})
		theirs := repo.tree(map[string]entry{"node": symlink("theirs")})
		results["symlink_to_file"] = repo.merge(base, ours, theirs)
		requireConflictContains(t, results["symlink_to_file"], "node")
	})

	t.Run("submodule_fast_forward", func(t *testing.T) {
		empty := repo.tree(nil)
		childBase := repo.commit(empty)
		childNext := repo.commit(empty, childBase)
		base := repo.tree(map[string]entry{"sub": gitlink(childBase)})
		ours := repo.tree(map[string]entry{"sub": gitlink(childNext)})
		theirs := base
		result := repo.merge(base, ours, theirs)
		requireClean(t, result)
		mode, oid := repo.pathOID(result.Tree, "sub")
		if mode != "160000" || oid != childNext {
			t.Fatalf("mode=%s oid=%s want=%s", mode, oid, childNext)
		}
		results["submodule_fast_forward"] = result
	})

	t.Run("submodule_divergent", func(t *testing.T) {
		empty := repo.tree(nil)
		childBase := repo.commit(empty)
		childOurs := repo.commit(empty, childBase)
		childTheirs := repo.commit(repo.tree(map[string]entry{"x": file("x")}), childBase)
		base := repo.tree(map[string]entry{"sub": gitlink(childBase)})
		ours := repo.tree(map[string]entry{"sub": gitlink(childOurs)})
		theirs := repo.tree(map[string]entry{"sub": gitlink(childTheirs)})
		results["submodule_divergent"] = repo.merge(base, ours, theirs)
		requireConflict(t, results["submodule_divergent"], "sub")
	})

	t.Run("attributes_text_eol", func(t *testing.T) {
		attributes := file("*.txt text eol=lf\n")
		base := repo.tree(map[string]entry{
			".gitattributes": attributes,
			"lines.txt":      file("one\r\nkeep-a\r\nkeep-b\r\nfour\r\n"),
		})
		ours := repo.tree(map[string]entry{
			".gitattributes": attributes,
			"lines.txt":      file("ONE\r\nkeep-a\r\nkeep-b\r\nfour\r\n"),
		})
		theirs := repo.tree(map[string]entry{
			".gitattributes": attributes,
			"lines.txt":      file("one\r\nkeep-a\r\nkeep-b\r\nFOUR\r\n"),
		})
		result := repo.merge(base, ours, theirs)
		requireClean(t, result)
		_, data := repo.readPath(result.Tree, "lines.txt")
		if string(data) != "ONE\r\nkeep-a\r\nkeep-b\r\nFOUR\r\n" {
			t.Fatalf("unexpected merged bytes %q", data)
		}
		results["attributes_text_eol"] = result

		repo.config("merge.renormalize", "true")
		renormalized := repo.merge(base, ours, theirs)
		requireClean(t, renormalized)
		_, normalizedData := repo.readPath(renormalized.Tree, "lines.txt")
		results["attributes_text_eol_renormalize"] = renormalized
		t.Logf("renormalize bytes=%q", normalizedData)
		repo.config("merge.renormalize", "false")
	})

	t.Run("attributes_minus_merge", func(t *testing.T) {
		attributes := file("*.lock -merge\n")
		base := repo.tree(map[string]entry{".gitattributes": attributes, "value.lock": file("base\n")})
		ours := repo.tree(map[string]entry{".gitattributes": attributes, "value.lock": file("ours\n")})
		theirs := repo.tree(map[string]entry{".gitattributes": attributes, "value.lock": file("theirs\n")})
		results["attributes_minus_merge"] = repo.merge(base, ours, theirs)
		requireConflict(t, results["attributes_minus_merge"], "value.lock")
	})

	t.Run("binary", func(t *testing.T) {
		base := repo.tree(map[string]entry{"value.bin": {mode: "100644", data: []byte{'a', 0, 'b'}}})
		ours := repo.tree(map[string]entry{"value.bin": {mode: "100644", data: []byte{'o', 0, 'b'}}})
		theirs := repo.tree(map[string]entry{"value.bin": {mode: "100644", data: []byte{'t', 0, 'b'}}})
		results["binary"] = repo.merge(base, ours, theirs)
		requireConflict(t, results["binary"], "value.bin")
	})

	t.Run("configured_custom_driver", func(t *testing.T) {
		attributes := file("*.drv merge=choice\n")
		base := repo.tree(map[string]entry{".gitattributes": attributes, "value.drv": file("base\n")})
		ours := repo.tree(map[string]entry{".gitattributes": attributes, "value.drv": file("ours\n")})
		theirs := repo.tree(map[string]entry{".gitattributes": attributes, "value.drv": file("theirs\n")})

		repo.config("merge.choice.name", "choose theirs")
		repo.config("merge.choice.driver", "cp %B %A")
		pickTheirs := repo.merge(base, ours, theirs)
		requireClean(t, pickTheirs)
		_, theirsData := repo.readPath(pickTheirs.Tree, "value.drv")
		if string(theirsData) != "theirs\n" {
			t.Fatalf("custom driver did not choose theirs: %q", theirsData)
		}
		results["custom_driver_theirs"] = pickTheirs

		repo.config("merge.choice.driver", "true")
		pickOurs := repo.merge(base, ours, theirs)
		requireClean(t, pickOurs)
		_, oursData := repo.readPath(pickOurs.Tree, "value.drv")
		if string(oursData) != "ours\n" {
			t.Fatalf("custom driver did not retain ours: %q", oursData)
		}
		if pickOurs.Tree == pickTheirs.Tree {
			t.Fatal("changing only Git config did not change the result tree")
		}
		results["custom_driver_ours"] = pickOurs
	})

	t.Run("noop_identity", func(t *testing.T) {
		base := repo.tree(map[string]entry{"a": file("base\n")})
		oursEntry := file("changed\n")
		oursEntry.mode = "100755"
		ours := repo.tree(map[string]entry{"a": oursEntry})
		result := repo.merge(base, ours, base)
		requireClean(t, result)
		if result.Tree != ours {
			t.Fatalf("merge(base=B, ours=T, theirs=B)=%s want %s", result.Tree, ours)
		}
		results["noop_identity"] = result
	})

	t.Run("fanin_own_bases_and_dedup", func(t *testing.T) {
		base := repo.tree(map[string]entry{"a": file("a0\n"), "b": file("b0\n")})
		first := repo.tree(map[string]entry{"a": file("a1\n"), "b": file("b0\n")})
		second := repo.tree(map[string]entry{"a": file("a1\n"), "b": file("b1\n")})
		changes := []changeSet{
			{id: changeSetID(base, first), base: base, result: first},
			{id: changeSetID(first, second), base: first, result: second},
			{id: changeSetID(base, first), base: base, result: first},
		}
		result, applied := repo.compose(base, changes)
		requireClean(t, result)
		if result.Tree != second || len(applied) != 2 {
			t.Fatalf("tree=%s want=%s applied=%v", result.Tree, second, applied)
		}
		results["fanin_own_bases_and_dedup"] = result
	})

	headBefore := repo.git("symbolic-ref", "HEAD")
	if _, err := os.Stat(filepath.Join(repo.gitDir, "index")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repository unexpectedly has an index: %v", err)
	}
	entries, err := os.ReadDir(repo.workTree)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != ".git" {
		t.Fatalf("merge-tree touched the empty work tree: %v", entries)
	}
	headAfter := repo.git("symbolic-ref", "HEAD")
	if headBefore != headAfter {
		t.Fatalf("HEAD changed: %q -> %q", headBefore, headAfter)
	}

	payload := struct {
		GitVersion string                 `json:"git_version"`
		Results    map[string]mergeResult `json:"results"`
	}{GitVersion: version, Results: results}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("FANIN_RESULT %s\n", encoded)
}

func newRepository(t *testing.T) *repository {
	t.Helper()
	workTree := filepath.Join(t.TempDir(), "repo")
	command := exec.Command("git", "init", "--quiet", "--object-format=sha1", workTree)
	command.Env = gitEnvironment()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	repo := &repository{t: t, gitDir: filepath.Join(workTree, ".git"), workTree: workTree}
	repo.config("core.autocrlf", "false")
	repo.config("core.filemode", "true")
	repo.config("merge.renormalize", "false")
	return repo
}

func (repo *repository) merge(base, ours, theirs string) mergeResult {
	repo.t.Helper()
	args := []string{
		"merge-tree", "--write-tree", "--merge-base=" + base,
		"--name-only",
	}
	if os.Getenv("FANIN_MESSAGES") != "1" {
		args = append(args, "--no-messages")
	}
	legacyOutput := os.Getenv("FANIN_LEGACY_OUTPUT") == "1"
	if !legacyOutput {
		args = append(args, "-z")
	}
	args = append(args, ours, theirs)
	command := repo.command(args...)
	if os.Getenv("FANIN_NO_ATTR_SOURCE") != "1" {
		command.Env = append(command.Env, "GIT_ATTR_SOURCE="+ours)
	}
	output, err := command.Output()
	exitCode := 0
	if err != nil {
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) {
			repo.t.Fatalf("merge-tree: %v", err)
		}
		exitCode = exitError.ExitCode()
		if exitCode != 1 {
			repo.t.Fatalf("merge-tree exit=%d stderr=%s", exitCode, exitError.Stderr)
		}
	}
	var fields [][]byte
	if legacyOutput {
		fields = bytes.Split(bytes.TrimSpace(output), []byte{'\n'})
	} else {
		fields = bytes.Split(bytes.TrimSuffix(output, []byte{0}), []byte{0})
	}
	if len(fields) == 0 || len(fields[0]) == 0 {
		repo.t.Fatalf("merge-tree produced no tree: %q", output)
	}
	result := mergeResult{Clean: exitCode == 0, Tree: string(fields[0])}
	if exitCode == 1 {
		for _, field := range fields[1:] {
			if len(field) != 0 {
				result.Paths = append(result.Paths, string(field))
			}
		}
		sort.Strings(result.Paths)
	}
	return result
}

func (repo *repository) compose(base string, changes []changeSet) (mergeResult, []string) {
	current := mergeResult{Clean: true, Tree: base}
	seen := map[string]bool{}
	var applied []string
	for _, change := range changes {
		if seen[change.id] {
			continue
		}
		seen[change.id] = true
		applied = append(applied, change.id)
		current = repo.merge(change.base, current.Tree, change.result)
		if !current.Clean {
			return current, applied
		}
	}
	return current, applied
}

func (repo *repository) tree(entries map[string]entry) string {
	repo.t.Helper()
	type treeNode struct {
		files    map[string]entry
		children map[string]*treeNode
	}
	root := &treeNode{files: map[string]entry{}, children: map[string]*treeNode{}}
	for path, item := range entries {
		parts := strings.Split(path, "/")
		node := root
		for _, directory := range parts[:len(parts)-1] {
			if node.children[directory] == nil {
				node.children[directory] = &treeNode{files: map[string]entry{}, children: map[string]*treeNode{}}
			}
			node = node.children[directory]
		}
		node.files[parts[len(parts)-1]] = item
	}
	var writeTree func(*treeNode) string
	writeTree = func(node *treeNode) string {
		var lines []string
		for name, child := range node.children {
			lines = append(lines, fmt.Sprintf("040000 tree %s\t%s\x00", writeTree(child), name))
		}
		for name, item := range node.files {
			oid := item.oid
			objectType := "blob"
			if item.mode == "160000" {
				objectType = "commit"
			} else if oid == "" {
				oid = repo.hashObject(item.data)
			}
			lines = append(lines, fmt.Sprintf("%s %s %s\t%s\x00", item.mode, objectType, oid, name))
		}
		sort.Strings(lines)
		return strings.TrimSpace(repo.gitInput(strings.Join(lines, ""), "mktree", "-z"))
	}
	return writeTree(root)
}

func (repo *repository) hashObject(data []byte) string {
	repo.t.Helper()
	command := repo.command("hash-object", "-w", "--stdin")
	command.Stdin = bytes.NewReader(data)
	output, err := command.CombinedOutput()
	if err != nil {
		repo.t.Fatalf("hash-object: %v\n%s", err, output)
	}
	return strings.TrimSpace(string(output))
}

func (repo *repository) commit(tree string, parents ...string) string {
	args := []string{"commit-tree", tree}
	for _, parent := range parents {
		args = append(args, "-p", parent)
	}
	return strings.TrimSpace(repo.gitInput("fixture\n", args...))
}

func (repo *repository) readPath(tree, path string) (string, []byte) {
	mode, oid := repo.pathOID(tree, path)
	return mode, []byte(repo.git("cat-file", "blob", oid))
}

func (repo *repository) pathOID(tree, path string) (string, string) {
	output := strings.TrimSpace(repo.git("ls-tree", tree, "--", path))
	fields := strings.Fields(output)
	if len(fields) < 3 {
		repo.t.Fatalf("ls-tree %s %s: %q", tree, path, output)
	}
	return fields[0], fields[2]
}

func (repo *repository) config(key, value string) {
	repo.t.Helper()
	repo.git("config", key, value)
}

func (repo *repository) git(args ...string) string {
	repo.t.Helper()
	output, err := repo.command(args...).CombinedOutput()
	if err != nil {
		repo.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func (repo *repository) gitInput(input string, args ...string) string {
	repo.t.Helper()
	command := repo.command(args...)
	command.Stdin = strings.NewReader(input)
	output, err := command.CombinedOutput()
	if err != nil {
		repo.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func (repo *repository) command(args ...string) *exec.Cmd {
	commandArgs := append([]string{"--git-dir", repo.gitDir, "--work-tree", repo.workTree}, args...)
	command := exec.Command("git", commandArgs...)
	command.Env = gitEnvironment()
	return command
}

func gitEnvironment() []string {
	environment := []string{
		"PATH=" + os.Getenv("PATH"),
		"TMPDIR=/tmp",
		"LC_ALL=C",
		"TZ=UTC",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=Partitur Spike",
		"GIT_AUTHOR_EMAIL=spike@example.invalid",
		"GIT_AUTHOR_DATE=2000-01-01T00:00:00Z",
		"GIT_COMMITTER_NAME=Partitur Spike",
		"GIT_COMMITTER_EMAIL=spike@example.invalid",
		"GIT_COMMITTER_DATE=2000-01-01T00:00:00Z",
	}
	return environment
}

func file(value string) entry {
	return entry{mode: "100644", data: []byte(value)}
}

func symlink(target string) entry {
	return entry{mode: "120000", data: []byte(target)}
}

func gitlink(oid string) entry {
	return entry{mode: "160000", oid: oid}
}

func changeSetID(base, result string) string {
	sum := sha256.Sum256([]byte(base + "\x00" + result))
	return hex.EncodeToString(sum[:])
}

func requireClean(t *testing.T, result mergeResult) {
	t.Helper()
	if !result.Clean {
		t.Fatalf("unexpected conflict: %+v", result)
	}
}

func requireConflict(t *testing.T, result mergeResult, paths ...string) {
	t.Helper()
	if result.Clean {
		t.Fatalf("unexpected clean merge: %+v", result)
	}
	actual := slices.Clone(result.Paths)
	sort.Strings(actual)
	sort.Strings(paths)
	if !slices.Equal(actual, paths) {
		t.Fatalf("conflicted paths=%v want=%v", actual, paths)
	}
}

func requireConflictContains(t *testing.T, result mergeResult, paths ...string) {
	t.Helper()
	if result.Clean {
		t.Fatalf("unexpected clean merge: %+v", result)
	}
	for _, path := range paths {
		if !slices.Contains(result.Paths, path) {
			t.Fatalf("conflicted paths=%v missing=%s", result.Paths, path)
		}
	}
}
