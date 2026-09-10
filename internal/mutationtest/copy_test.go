package mutationtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCopyRepositoryRootExclusions(t *testing.T) {
	for _, name := range []string{".git", ".partitur"} {
		for _, kind := range []string{"directory", "file", "symlink"} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				source := t.TempDir()
				excluded := filepath.Join(source, name)
				switch kind {
				case "directory":
					writeCopyFixture(t, filepath.Join(excluded, ".gitignore"), "tracked ignore", 0o644)
					writeCopyFixture(t, filepath.Join(excluded, "cast.yaml"), "tracked state", 0o644)
				case "file":
					writeCopyFixture(t, excluded, "state file", 0o644)
				case "symlink":
					target := t.TempDir()
					writeCopyFixture(t, filepath.Join(target, "cast.yaml"), "tracked state", 0o644)
					if err := os.Symlink(target, excluded); err != nil {
						t.Fatal(err)
					}
				}
				sentinel := "z-after-" + name[1:]
				writeCopyFixture(t, filepath.Join(source, sentinel), "sentinel", 0o644)
				destination := filepath.Join(t.TempDir(), "copy")
				if err := CopyRepository(destination, source); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(filepath.Join(destination, name)); !os.IsNotExist(err) {
					t.Errorf("excluded %s exists or cannot be checked: %v", name, err)
				}
				assertCopyContents(t, filepath.Join(destination, sentinel), "sentinel")
				entries, err := os.ReadDir(destination)
				if err != nil {
					t.Fatal(err)
				}
				var names []string
				for _, entry := range entries {
					names = append(names, entry.Name())
				}
				if !reflect.DeepEqual(names, []string{sentinel}) {
					t.Errorf("copied entries = %v, want [%s]", names, sentinel)
				}
			})
		}
	}
}

func TestCopyRepositoryPreservesEntries(t *testing.T) {
	source := t.TempDir()
	files := map[string]os.FileMode{
		"nested/.git/keep":           0o644,
		"nested/.partitur/cast.yaml": 0o644,
		"other/.git":                 0o644,
		"other/.partitur":            0o644,
		".codegraph/graph.json":      0o644,
		".tmp/keep":                  0o644,
		".omo/keep":                  0o644,
		"build/keep":                 0o644,
		".gitignore":                 0o644,
		"mode-0644":                  0o644,
		"mode-0600":                  0o600,
		"mode-0755":                  0o755,
	}
	for name, mode := range files {
		writeCopyFixture(t, filepath.Join(source, name), name, mode)
	}
	links := map[string]string{"file-link": "mode-0644", "directory-link": "nested", "dangling-link": "missing"}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(source, name)); err != nil {
			t.Fatal(err)
		}
	}
	destination := filepath.Join(t.TempDir(), "copy")
	if err := CopyRepository(destination, source); err != nil {
		t.Fatal(err)
	}
	for name, mode := range files {
		path := filepath.Join(destination, name)
		assertCopyContents(t, path, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != mode {
			t.Errorf("%s permissions = %04o, want %04o", name, info.Mode().Perm(), mode)
		}
	}
	for name, target := range links {
		actual, err := os.Readlink(filepath.Join(destination, name))
		if err != nil || actual != target {
			t.Errorf("%s link = %q, %v; want %q", name, actual, err, target)
		}
	}
	for _, name := range []string{".", "nested", "nested/.git", "nested/.partitur", "other", ".codegraph", ".tmp", ".omo", "build"} {
		info, err := os.Stat(filepath.Join(destination, name))
		if err != nil {
			t.Fatal(err)
		}
		if !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Errorf("%s mode = %s, want directory 0700", name, info.Mode())
		}
	}
}

func TestCopyRepositoryRejectsDestination(t *testing.T) {
	source := t.TempDir()
	writeCopyFixture(t, filepath.Join(source, "keep"), "unchanged", 0o644)
	existing := t.TempDir()
	writeCopyFixture(t, filepath.Join(existing, "keep"), "unchanged", 0o644)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(source, alias); err != nil {
		t.Fatal(err)
	}
	for name, destination := range map[string]string{
		"same":             source,
		"inside":           filepath.Join(source, "copy"),
		"deep-inside":      filepath.Join(source, "new", "copy"),
		"alias-inside":     filepath.Join(alias, "new", "copy"),
		"existing":         existing,
		"existing-file":    filepath.Join(existing, "keep"),
		"existing-symlink": alias,
	} {
		t.Run(name, func(t *testing.T) {
			if err := CopyRepository(destination, source); err == nil {
				t.Fatal("accepted invalid destination")
			}
		})
	}
	assertCopyContents(t, filepath.Join(source, "keep"), "unchanged")
	assertCopyContents(t, filepath.Join(existing, "keep"), "unchanged")
	if _, err := os.Stat(filepath.Join(source, "new")); !os.IsNotExist(err) {
		t.Errorf("rejected destination created source entries: %v", err)
	}
}

func TestCopyRepositoryRejectsSpecialFile(t *testing.T) {
	source := t.TempDir()
	if output, err := exec.Command("mkfifo", filepath.Join(source, "fifo")).CombinedOutput(); err != nil {
		t.Skipf("cannot create FIFO on this platform: %v: %s", err, output)
	}
	if err := CopyRepository(filepath.Join(t.TempDir(), "copy"), source); err == nil {
		t.Fatal("accepted unsupported FIFO")
	}
}

func writeCopyFixture(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func assertCopyContents(t *testing.T, path, want string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != want {
		t.Errorf("%s contents = %q, %v; want %q", path, contents, err, want)
	}
}
