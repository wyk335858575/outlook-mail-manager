package datakey

import (
	"errors"
	"testing"
)

func TestStoreRequiresUnlockAndNotifiesOnceUnlocked(t *testing.T) {
	store := New(nil)
	if _, err := store.SealString("secret", "field"); !errors.Is(err, ErrLocked) {
		t.Fatalf("SealString() error = %v, want ErrLocked", err)
	}
	notifications := 0
	store.OnUnlock(func() { notifications++ })
	if err := store.Unlock(make([]byte, 32)); err != nil {
		t.Fatalf("Unlock() error = %v", err)
	}
	ciphertext, err := store.SealString("secret", "field")
	if err != nil {
		t.Fatalf("SealString() error = %v", err)
	}
	plaintext, err := store.OpenString(ciphertext, "field")
	if err != nil || plaintext != "secret" {
		t.Fatalf("OpenString() = %q, error = %v", plaintext, err)
	}
	if notifications != 1 {
		t.Fatalf("unlock notifications = %d, want 1", notifications)
	}
}
