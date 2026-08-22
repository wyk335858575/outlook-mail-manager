package appversion

import "testing"

func TestParseAcceptsStrictThreePartVersions(t *testing.T) {
	for _, value := range []string{"1.0.0", "v1.0.10", "1.1.0", "0.11.0"} {
		if _, err := Parse(value); err != nil {
			t.Fatalf("Parse(%q) error = %v", value, err)
		}
	}
	for _, value := range []string{"", "1", "1.0", "1.0.0.1", "1.0.0-beta", "01.0.0", "v"} {
		if _, err := Parse(value); err == nil {
			t.Fatalf("Parse(%q) unexpectedly succeeded", value)
		}
	}
}

func TestGreaterUsesNumericComponents(t *testing.T) {
	if !Greater("1.0.0", "0.11.0") || !Greater("1.0.10", "1.0.9") || !Greater("1.1.0", "1.0.9") {
		t.Fatal("Greater did not recognize a newer version")
	}
	for _, pair := range [][2]string{{"1.0.0", "1.0.0"}, {"1.0.1", "1.0.2"}, {"bad", "1.0.0"}} {
		if Greater(pair[0], pair[1]) {
			t.Fatalf("Greater(%q, %q) = true", pair[0], pair[1])
		}
	}
}
