package docclause

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
)

type ActivationPins struct {
	MarkedBlob                  string `json:"marked_blob"`
	OrderedClassificationSHA256 string `json:"ordered_classification_sha256"`
}

func ValidateActivation(documentPath string, source, marked []byte, inputBlob string, regions []Region, registry Registry) error {
	if registry.Activation == nil {
		return fmt.Errorf("baseline activation is absent")
	}
	digest, err := ClassificationDigest(documentPath, source, inputBlob, regions, registry)
	if err != nil {
		return err
	}
	materialized, err := Materialize(documentPath, source, inputBlob, regions, registry)
	if err != nil {
		return err
	}
	if !bytes.Equal(marked, materialized) {
		return fmt.Errorf("marked document does not equal materialized classifications")
	}
	markedBlob := GitBlobID(marked)
	if registry.Activation.MarkedBlob != markedBlob {
		return fmt.Errorf("marked blob %q does not match activation pin %q", markedBlob, registry.Activation.MarkedBlob)
	}
	if registry.Activation.OrderedClassificationSHA256 != digest {
		return fmt.Errorf("ordered classification digest %q does not match activation pin %q", digest, registry.Activation.OrderedClassificationSHA256)
	}
	return nil
}

func GitBlobID(contents []byte) string {
	hash := sha1.New() // Git SHA-1 object identity, not a cryptographic security decision.
	fmt.Fprintf(hash, "blob %d%c", len(contents), byte(0))
	_, _ = hash.Write(contents)
	return hex.EncodeToString(hash.Sum(nil))
}
