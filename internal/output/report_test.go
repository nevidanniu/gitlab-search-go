package output

import (
	"strings"
	"testing"
)

func TestMaskTokenHidesSecret(t *testing.T) {
	secret := "glpat-3FcWH7hTESTTOKENabc"
	got := maskToken(secret)
	if strings.Contains(got, secret[4:]) {
		t.Fatalf("masked token still leaks: %q", got)
	}
	if !strings.HasPrefix(got, "glp") || !strings.HasSuffix(got, "abc") {
		t.Fatalf("want first/last characters kept, got %q", got)
	}
	if maskToken("short") != "*****" || maskToken("") != "(empty)" {
		t.Fatalf("short/empty handling: %q %q", maskToken("short"), maskToken(""))
	}
}

func TestQuoteLinePrefersMatchingLine(t *testing.T) {
	data := "context line\n\t  needle in here\nanother"
	if got := quoteLine(data, "NEEDLE"); got != "needle in here" {
		t.Fatalf("got %q", got)
	}
	if got := quoteLine(data, "absent"); got != "context line" {
		t.Fatalf("fallback should be the first line, got %q", got)
	}
	long := strings.Repeat("x", 200)
	if got := quoteLine(long, "x"); len(got) != 163 {
		t.Fatalf("want truncation at 160 runes plus ellipsis, got %d", len(got))
	}
}
