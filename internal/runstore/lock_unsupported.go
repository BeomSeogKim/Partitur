//go:build !linux && !darwin

package runstore

import "errors"

type fileLock struct{}

func acquireFileLock(string) (*fileLock, error) {
	return nil, errors.New("repository state lock is unsupported on this platform")
}

func (*fileLock) release() {}
