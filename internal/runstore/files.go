package runstore

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
)

// PublishImmutable publishes exact bytes without overwriting an existing name.
func (transaction *Txn) PublishImmutable(
	path Path,
	contents []byte,
	expectedHash Hash,
) (DurabilityReceipt, error) {
	if err := transaction.requireReceiptAddress(); err != nil {
		return DurabilityReceipt{}, err
	}
	if digest(contents) != string(expectedHash) {
		return DurabilityReceipt{}, ErrHashMismatch
	}
	target, err := transaction.resolve(path)
	if err != nil {
		return DurabilityReceipt{}, err
	}
	parent := filepath.Dir(target)
	if err := transaction.store.fs.MkdirAll(parent, 0o700); err != nil {
		return DurabilityReceipt{}, fmt.Errorf("create publication directory: %w", err)
	}
	existing, err := transaction.store.fs.ReadFile(target)
	switch {
	case err == nil:
		if digest(existing) != string(expectedHash) || !bytes.Equal(existing, contents) {
			return DurabilityReceipt{}, ErrImmutablePublicationConflict
		}
		if err := transaction.store.fs.SyncFile(target); err != nil {
			return DurabilityReceipt{}, fmt.Errorf("sync existing publication: %w", err)
		}
		if err := transaction.store.fs.SyncDir(parent); err != nil {
			return DurabilityReceipt{}, fmt.Errorf("sync publication directory: %w", err)
		}
		receipt := transaction.newReceipt(faultpoint.FilePublication)
		receipt.Mutation.Path = relativeToRoot(transaction.store.root, target)
		return transaction.observeReceipt(receipt), nil
	case !errors.Is(err, fs.ErrNotExist):
		return DurabilityReceipt{}, fmt.Errorf("read publication target: %w", err)
	}

	temporary, err := transaction.store.fs.WriteTemp(parent, "."+filepath.Base(target)+".tmp-*", contents, 0o600)
	if err != nil {
		return DurabilityReceipt{}, fmt.Errorf("write publication temporary: %w", err)
	}
	cleanup := func() {
		_ = transaction.store.fs.Remove(temporary)
	}
	if err := transaction.store.fs.SyncFile(temporary); err != nil {
		cleanup()
		return DurabilityReceipt{}, fmt.Errorf("sync publication temporary: %w", err)
	}
	if current, err := transaction.store.fs.ReadFile(temporary); err != nil || digest(current) != string(expectedHash) {
		cleanup()
		if err != nil {
			return DurabilityReceipt{}, fmt.Errorf("verify publication temporary: %w", err)
		}
		return DurabilityReceipt{}, ErrHashMismatch
	}
	if err := transaction.store.fs.Rename(temporary, target); err != nil {
		cleanup()
		return DurabilityReceipt{}, fmt.Errorf("rename publication: %w", err)
	}
	if err := transaction.store.fs.SyncDir(parent); err != nil {
		return DurabilityReceipt{}, fmt.Errorf("sync publication directory: %w", err)
	}
	receipt := transaction.newReceipt(faultpoint.FilePublication)
	receipt.Mutation.Path = relativeToRoot(transaction.store.root, target)
	return transaction.observeReceipt(receipt), nil
}

// Quarantine durably moves a run-local source to its content-addressed path.
func (transaction *Txn) Quarantine(source Path) (QuarantineResult, error) {
	if err := transaction.requireReceiptAddress(); err != nil {
		return QuarantineResult{}, err
	}
	if !kindPattern.MatchString(transaction.quarantineKind) {
		return QuarantineResult{}, ErrQuarantineKindRequired
	}
	sourcePath, err := transaction.resolve(source)
	if err != nil {
		return QuarantineResult{}, err
	}
	contents, err := transaction.store.fs.ReadFile(sourcePath)
	if err != nil {
		return QuarantineResult{}, fmt.Errorf("read quarantine source: %w", err)
	}
	contentHash := strings.TrimPrefix(digest(contents), "sha256:")
	destination := Path(filepath.ToSlash(filepath.Join(
		"quarantine",
		transaction.quarantineKind,
		contentHash,
		filepath.Base(sourcePath),
	)))
	destinationPath, err := transaction.resolve(destination)
	if err != nil {
		return QuarantineResult{}, err
	}
	if present, err := exists(transaction.store.fs, destinationPath); err != nil {
		return QuarantineResult{}, err
	} else if present {
		return QuarantineResult{}, ErrImmutablePublicationConflict
	}
	if err := transaction.store.fs.MkdirAll(filepath.Dir(destinationPath), 0o700); err != nil {
		return QuarantineResult{}, fmt.Errorf("create quarantine directory: %w", err)
	}
	if err := transaction.store.fs.Rename(sourcePath, destinationPath); err != nil {
		return QuarantineResult{}, fmt.Errorf("rename quarantine source: %w", err)
	}
	if err := transaction.store.fs.SyncDir(filepath.Dir(sourcePath)); err != nil {
		return QuarantineResult{}, fmt.Errorf("sync quarantine source directory: %w", err)
	}
	if err := transaction.store.fs.SyncDir(filepath.Dir(destinationPath)); err != nil {
		return QuarantineResult{}, fmt.Errorf("sync quarantine destination directory: %w", err)
	}
	receipt := transaction.newReceipt(faultpoint.DurableQuarantine)
	receipt.Mutation.Source = relativeToRoot(transaction.store.root, sourcePath)
	receipt.Mutation.Destination = relativeToRoot(transaction.store.root, destinationPath)
	return QuarantineResult{
		Source:      source,
		Destination: destination,
		Receipt:     transaction.observeReceipt(receipt),
	}, nil
}

// RemoveDurable removes a run-local path and syncs its parent directory.
func (transaction *Txn) RemoveDurable(path Path) (DurabilityReceipt, error) {
	if err := transaction.requireReceiptAddress(); err != nil {
		return DurabilityReceipt{}, err
	}
	target, err := transaction.resolve(path)
	if err != nil {
		return DurabilityReceipt{}, err
	}
	if err := transaction.store.fs.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return DurabilityReceipt{}, fmt.Errorf("remove durable path: %w", err)
	}
	if err := transaction.store.fs.SyncDir(filepath.Dir(target)); err != nil {
		return DurabilityReceipt{}, fmt.Errorf("sync removal directory: %w", err)
	}
	receipt := transaction.newReceipt(faultpoint.DurableRemoval)
	receipt.Mutation.Path = relativeToRoot(transaction.store.root, target)
	return transaction.observeReceipt(receipt), nil
}

func digest(contents []byte) string {
	hash := sha256.Sum256(contents)
	return fmt.Sprintf("sha256:%x", hash)
}
