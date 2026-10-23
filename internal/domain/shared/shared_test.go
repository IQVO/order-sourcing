package shared

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestInvalidAndCorruptWrapTheirSentinel(t *testing.T) {
	err := Invalid("field %s is %d", "x", 3)
	if !errors.Is(err, ErrValidation) || errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("Invalid must wrap ErrValidation only: %v", err)
	}
	if err.Error() != "validation failed: field x is 3" {
		t.Fatalf("message = %q", err.Error())
	}
	err = Corrupt("row %d", 7)
	if !errors.Is(err, ErrInvalidSnapshot) || errors.Is(err, ErrValidation) {
		t.Fatalf("Corrupt must wrap ErrInvalidSnapshot only: %v", err)
	}
	if err.Error() != "invalid snapshot: row 7" {
		t.Fatalf("message = %q", err.Error())
	}
	if errors.Is(ErrIllegalTransition, ErrValidation) || errors.Is(ErrIllegalTransition, ErrInvalidSnapshot) {
		t.Fatal("sentinels must be distinct")
	}
}

func TestValidID(t *testing.T) {
	good := []string{"a", "z", "A", "Z", "0", "9", "0z9A", "asg-0f3a91", "a.b_c:d-e", "n" + strings.Repeat("x", MaxID-1)}
	for _, s := range good {
		if !ValidID(s) {
			t.Errorf("ValidID(%q) = false, want true", s)
		}
	}
	bad := []string{"", "-a", ".a", "_a", ":a", " a", "a b", "a/b", "a\x00", "é", "a" + strings.Repeat("x", MaxID),
		"@", "[", "`", "{", "/", ":" /* leading ':' */, "a@", "a[", "a`", "a{", "a/", "a;"}
	for _, s := range bad {
		if ValidID(s) {
			t.Errorf("ValidID(%q) = true, want false", s)
		}
	}
}

func TestValidText(t *testing.T) {
	if !ValidText("SP-CAJAMAR 01") || !ValidText(strings.Repeat("é", MaxText)) {
		t.Fatal("plain text and the limit must be accepted")
	}
	for _, s := range []string{"", " ", " a", "a ", "a\tb", "a\x7fb", "a\x1fb", strings.Repeat("é", MaxText+1)} {
		if ValidText(s) {
			t.Errorf("ValidText(%q) = true, want false", s)
		}
	}
	if !ValidTextN("abc", 3) || ValidTextN("abcd", 3) {
		t.Fatal("ValidTextN must honour its limit inclusively")
	}
	if !ValidText("a b") || !ValidText("a\u00a0b") {
		t.Fatal("inner spaces are fine")
	}
}

func TestUTCAndHeader(t *testing.T) {
	if !UTC(time.Time{}).IsZero() {
		t.Fatal("zero stays zero")
	}
	brt := time.FixedZone("BRT", -3*3600)
	at := time.Date(2026, 10, 22, 9, 0, 0, 0, brt)
	got := UTC(at)
	if got.Location() != time.UTC || !got.Equal(at) {
		t.Fatalf("UTC() = %v", got)
	}
	h := Header{Version: 4, At: got}
	if h.AggregateVersion() != 4 || !h.OccurredAt().Equal(at) {
		t.Fatalf("header accessors wrong: %+v", h)
	}
	if MaxInt32 != 1<<31-1 {
		t.Fatal("MaxInt32 must be the Postgres integer ceiling")
	}
}
