package runstore

import (
	"io/fs"
	"os"
)

// FileSystem is the filesystem dependency used by Store. It is exported only
// so command-package tests can inject failures at the CLI boundary; runstore
// remains an internal package.
type FileSystem interface {
	MkdirAll(string, fs.FileMode) error
	ReadFile(string) ([]byte, error)
	WriteTemp(string, string, []byte, fs.FileMode) (string, error)
	Append(string, []byte, fs.FileMode) error
	SyncFile(string) error
	SyncDir(string) error
	Rename(string, string) error
	Remove(string) error
	Truncate(string, int64) error
	Stat(string) (fs.FileInfo, error)
}

type fsOperations = FileSystem

type realFS struct{}

func (realFS) MkdirAll(path string, mode fs.FileMode) error {
	return os.MkdirAll(path, mode)
}

func (realFS) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (realFS) WriteTemp(directory, pattern string, contents []byte, mode fs.FileMode) (string, error) {
	file, err := os.CreateTemp(directory, pattern)
	if err != nil {
		return "", err
	}
	name := file.Name()
	cleanup := func() {
		_ = file.Close()
		_ = os.Remove(name)
	}
	if err := file.Chmod(mode); err != nil {
		cleanup()
		return "", err
	}
	if _, err := file.Write(contents); err != nil {
		cleanup()
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

func (realFS) Append(path string, contents []byte, mode fs.FileMode) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func (realFS) SyncFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func (realFS) SyncDir(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (realFS) Rename(source, destination string) error {
	return os.Rename(source, destination)
}

func (realFS) Remove(path string) error {
	return os.Remove(path)
}

func (realFS) Truncate(path string, size int64) error {
	return os.Truncate(path, size)
}

func (realFS) Stat(path string) (fs.FileInfo, error) {
	return os.Stat(path)
}
