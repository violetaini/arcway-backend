package handler

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyBinaryChecksum(t *testing.T) {
	path := filepath.Join(t.TempDir(), "arcway")
	content := []byte("verified release asset")
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	expected := fmt.Sprintf("%x", sha256.Sum256(content))
	if err := verifyBinaryChecksum(path, expected); err != nil {
		t.Fatalf("valid checksum rejected: %v", err)
	}
	if err := verifyBinaryChecksum(path, ""); err == nil {
		t.Fatal("missing checksum accepted")
	}
	if err := verifyBinaryChecksum(path, fmt.Sprintf("%064x", 1)); err == nil {
		t.Fatal("mismatched checksum accepted")
	}
}
