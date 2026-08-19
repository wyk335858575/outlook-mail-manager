package mail

import (
	"strings"
	"testing"
)

func TestSanitizeMessageHTMLRemovesExecutableContent(t *testing.T) {
	result, err := sanitizeMessageHTML(`
		<script>alert(1)</script>
		<form action="https://attacker.example"><input name="secret"></form>
		<a href="javascript:alert(1)" onclick="alert(2)">open</a>
		<p onmouseover="alert(3)">safe text</p>
	`, false)
	if err != nil {
		t.Fatalf("sanitizeMessageHTML() error = %v", err)
	}
	for _, forbidden := range []string{"<script", "<form", "<input", "href=", "javascript:", "onclick", "onmouseover"} {
		if strings.Contains(strings.ToLower(result), forbidden) {
			t.Fatalf("sanitized HTML contains %q: %s", forbidden, result)
		}
	}
	if !strings.Contains(result, "safe text") {
		t.Fatalf("sanitized HTML lost safe text: %s", result)
	}
}

func TestSanitizeMessageHTMLBlocksRemoteImagesByDefault(t *testing.T) {
	input := `<img src="https://tracker.example/pixel.png"><img src="data:image/png;base64,iVBORw0KGgo=">`
	result, err := sanitizeMessageHTML(input, false)
	if err != nil {
		t.Fatalf("sanitizeMessageHTML() error = %v", err)
	}
	if strings.Contains(result, "tracker.example") {
		t.Fatalf("remote image survived default policy: %s", result)
	}
	if !strings.Contains(result, "data:image/png;base64,iVBORw0KGgo=") {
		t.Fatalf("embedded data image was removed: %s", result)
	}
}

func TestSanitizeMessageHTMLAllowsOnlyHTTPSRemoteImagesAfterConsent(t *testing.T) {
	input := `<img src="https://images.example/banner.png"><img src="http://images.example/insecure.png"><img src="javascript:alert(1)">`
	result, err := sanitizeMessageHTML(input, true)
	if err != nil {
		t.Fatalf("sanitizeMessageHTML() error = %v", err)
	}
	if !strings.Contains(result, "https://images.example/banner.png") {
		t.Fatalf("HTTPS image was removed: %s", result)
	}
	for _, forbidden := range []string{"http://images.example", "javascript:"} {
		if strings.Contains(result, forbidden) {
			t.Fatalf("unsafe image URL survived: %s", result)
		}
	}
}
