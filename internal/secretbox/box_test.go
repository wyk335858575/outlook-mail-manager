package secretbox

import (
	"crypto/rand"
	"testing"
)

func TestSealAndOpenString(t *testing.T) {
	box, err := New(make([]byte, 32), rand.Reader)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	sealed, err := box.SealString("totp-secret", "admin:1:totp")
	if err != nil {
		t.Fatalf("SealString() error = %v", err)
	}
	opened, err := box.OpenString(sealed, "admin:1:totp")
	if err != nil {
		t.Fatalf("OpenString() error = %v", err)
	}
	if opened != "totp-secret" {
		t.Fatalf("OpenString() = %q", opened)
	}

	if _, err := box.OpenString(sealed, "admin:2:totp"); err == nil {
		t.Fatal("OpenString() accepted different associated data")
	}
}
