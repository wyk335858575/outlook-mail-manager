package auth

import (
	"crypto/rand"
	"testing"
)

func TestPasswordHashRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse 电池订书钉", rand.Reader)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}

	valid, err := VerifyPassword(hash, "correct horse 电池订书钉")
	if err != nil || !valid {
		t.Fatalf("VerifyPassword(correct) = %v, %v", valid, err)
	}
	valid, err = VerifyPassword(hash, "wrong password")
	if err != nil || valid {
		t.Fatalf("VerifyPassword(wrong) = %v, %v", valid, err)
	}
}

func TestPasswordPolicy(t *testing.T) {
	if err := ValidatePassword("too short"); err == nil {
		t.Fatal("ValidatePassword() accepted a short password")
	}
	if err := ValidatePassword("这是十二个以上字符的管理员密码"); err != nil {
		t.Fatalf("ValidatePassword() rejected Unicode passphrase: %v", err)
	}
}

func BenchmarkPasswordHash(b *testing.B) {
	for b.Loop() {
		if _, err := HashPassword("benchmark password phrase", rand.Reader); err != nil {
			b.Fatal(err)
		}
	}
}
