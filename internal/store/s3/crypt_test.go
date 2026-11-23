package s3_test

import (
	"context"
	"errors"
	"testing"

	"github.com/JGugino/world-hopper/internal/crypt"
	"github.com/JGugino/world-hopper/internal/store/storetest"
)

// Encryption is provider independent: the same wrapper works over S3.
func TestEncryptionOverS3(t *testing.T) {
	s, _ := setup(t, "")
	ctx := context.Background()
	h, k, err := crypt.NewHeader("correct horse battery", crypt.Params{Time: 1, MemKiB: 64, Threads: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := crypt.WriteHeader(ctx, s, h); err != nil {
		t.Fatal(err)
	}
	storetest.Contract(t, crypt.New(s, k))

	h2, err := crypt.ReadHeader(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h2.Unlock("wrong passphrase!"); !errors.Is(err, crypt.ErrWrongPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
}
