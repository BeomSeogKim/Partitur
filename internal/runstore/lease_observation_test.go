package runstore

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
)

func TestReadLeaseIsReadOnlyWhenRunDoesNotExist(t *testing.T) {
	root := t.TempDir()
	store, err := New(root, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	_, present, err := store.ReadLease("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if present {
		t.Fatal("ReadLease() reported a lease that was never written")
	}
	_, err = os.Stat(filepath.Join(root, ".partitur"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadLease() created run-store state: stat error = %v", err)
	}
}
