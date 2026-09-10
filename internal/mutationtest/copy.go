package mutationtest

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// CopyRepository copies source into a fresh destination outside source, excluding
// only the root .git and .partitur entries. Symlinks and file permissions survive.
func CopyRepository(destination, source string) error {
	source, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("source is not a directory: %s", source)
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("destination already exists: %s", destination)
	} else if !os.IsNotExist(err) {
		return err
	}
	// Resolve the existing ancestor so a symlink alias cannot hide containment.
	ancestor := filepath.Dir(destination)
	for {
		resolved, err := filepath.EvalSymlinks(ancestor)
		if err == nil {
			suffix, err := filepath.Rel(ancestor, destination)
			if err != nil {
				return err
			}
			destination = filepath.Join(resolved, suffix)
			break
		}
		if !os.IsNotExist(err) || filepath.Dir(ancestor) == ancestor {
			return err
		}
		ancestor = filepath.Dir(ancestor)
	}
	relative, err := filepath.Rel(source, destination)
	if err != nil {
		return err
	}
	if relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("destination must be outside source: %s", destination)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		return err
	}
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == ".git" || relative == ".partitur" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if relative == "." {
			return os.Chmod(destination, 0o700)
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			if err := os.Mkdir(target, 0o700); err != nil {
				return err
			}
			return os.Chmod(target, 0o700)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported file type at %s: %s", path, info.Mode())
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, contents, info.Mode().Perm()); err != nil {
			return err
		}
		return os.Chmod(target, info.Mode().Perm())
	})
}
