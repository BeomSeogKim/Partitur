package supervision

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

type lease struct {
	PID   int    `json:"pid"`
	Start string `json:"start"`
	Token string `json:"token"`
	Epoch uint64 `json:"epoch"`
}

type authority struct {
	Token    string `json:"token"`
	Epoch    uint64 `json:"epoch"`
	Terminal bool   `json:"terminal"`
}

func acquireLease(directory string) (lease, error) {
	var acquired lease
	err := withStateLock(directory, func() error {
		currentLease, err := readLease(directory)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil {
			// Fail closed: reclaim only when the holder is verifiably gone. An
			// unverifiable holder is not a dead holder.
			live, matchErr := identityMatches(currentLease.PID, currentLease.Start)
			if matchErr != nil {
				return fmt.Errorf("cannot verify current lease holder: %w", matchErr)
			}
			if live {
				return errors.New("lease is held by a live incarnation")
			}
		}
		owner, err := currentIdentity()
		if err != nil {
			return err
		}
		state, err := readAuthority(directory)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		token, err := randomToken()
		if err != nil {
			return err
		}
		acquired = lease{
			PID: owner.PID, Start: owner.Start, Token: token, Epoch: state.Epoch + 1,
		}
		if err := writeJSONAtomic(filepath.Join(directory, "authority.json"), authority{
			Token: acquired.Token, Epoch: acquired.Epoch,
		}); err != nil {
			return err
		}
		return writeJSONAtomic(filepath.Join(directory, "driver.lease"), acquired)
	})
	return acquired, err
}

func mutate(directory string, owner lease, value string) error {
	return withStateLock(directory, func() error {
		state, err := readAuthority(directory)
		if err != nil {
			return err
		}
		currentLease, err := readLease(directory)
		if err != nil {
			return err
		}
		live, matchErr := identityMatches(owner.PID, owner.Start)
		if matchErr != nil {
			return fmt.Errorf("cannot verify mutating incarnation: %w", matchErr)
		}
		if state.Terminal ||
			state.Token != owner.Token || state.Epoch != owner.Epoch ||
			currentLease.Token != owner.Token || currentLease.Epoch != owner.Epoch ||
			!live {
			return errors.New("lease incarnation is fenced")
		}
		file, err := os.OpenFile(filepath.Join(directory, "mutations"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = fmt.Fprintln(file, value)
		return err
	})
}

func fence(directory string, expected lease) error {
	return withStateLock(directory, func() error {
		state, err := readAuthority(directory)
		if err != nil {
			return err
		}
		currentLease, err := readLease(directory)
		if err != nil {
			return err
		}
		if state.Token != expected.Token || state.Epoch != expected.Epoch ||
			currentLease.Token != expected.Token || currentLease.Epoch != expected.Epoch {
			return errors.New("lease changed before fence")
		}
		live, matchErr := identityMatches(currentLease.PID, currentLease.Start)
		if matchErr != nil {
			return fmt.Errorf("cannot verify lease owner before fence: %w", matchErr)
		}
		if !live {
			return errors.New("lease owner identity changed before fence")
		}
		state.Epoch++
		state.Token = ""
		state.Terminal = true
		if err := writeJSONAtomic(filepath.Join(directory, "authority.json"), state); err != nil {
			return err
		}
		return os.Remove(filepath.Join(directory, "driver.lease"))
	})
}

func withStateLock(directory string, operation func() error) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	lockFile, err := os.OpenFile(filepath.Join(directory, "state.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lockFile.Close()
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
	return operation()
}

func readLease(directory string) (lease, error) {
	var value lease
	err := readJSON(filepath.Join(directory, "driver.lease"), &value)
	return value, err
}

func readAuthority(directory string) (authority, error) {
	var value authority
	err := readJSON(filepath.Join(directory, "authority.json"), &value)
	return value, err
}

func readJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func writeJSONAtomic(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func randomToken() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}
