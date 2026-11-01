package http

import "testing"

// TestSanitizeLogStripsCRLF proves the CWE-117 log-injection fix: a crafted
// value containing CR/LF (the classic forged-log-line payload) has both
// stripped, so it can never render as a second, separate log line.
func TestSanitizeLogStripsCRLF(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"no control chars", "/orders/abc-123", "/orders/abc-123"},
		{"embedded newline", "/orders/abc\n[INFO] forged line", "/orders/abc[INFO] forged line"},
		{"embedded CRLF", "/orders/abc\r\n[INFO] forged line", "/orders/abc[INFO] forged line"},
		{"embedded bare CR", "/orders/abc\rforged", "/orders/abcforged"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeLog(tc.in); got != tc.want {
				t.Fatalf("sanitizeLog(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
