package http

import "strings"

// logReplacer strips the characters that let an untrusted value forge a log
// line (CWE-117).
var logReplacer = strings.NewReplacer("\r", "", "\n", "")

// sanitizeLog removes CR and LF from an untrusted value (request path, error
// text derived from request data) before it is logged (CodeQL
// go/log-injection).
func sanitizeLog(s string) string { return logReplacer.Replace(s) }
