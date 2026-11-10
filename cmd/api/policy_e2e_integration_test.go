//go:build integration

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

const ceTypePolicy = "com.retailnetwork.commerce.order-sourcing.policy.SourcingPolicyPublished"

func policyOf(t *testing.T, a api, method, key, body string) (int, map[string]any) {
	t.Helper()
	code, _, raw := a.do(method, "/sourcing-policy", key, body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return code, out
}

// exerciseSourcingPolicy proves the REST route, its idempotency, the Postgres versions and the
// analytics-only event end to end on the real wiring (ADR 0014).
func exerciseSourcingPolicy(t *testing.T, a api, brokers []string) {
	t.Helper()
	if code, got := policyOf(t, a, "GET", "", ""); code != 200 || got["version"] != 0.0 || got["publishedAt"] != nil {
		t.Fatalf("the default policy = %d %v", code, got)
	}
	if code, got := policyOf(t, a, "PUT", "", `{"maxCostDeviationPercent":20}`); code != 400 || !strings.Contains(asString(got["type"]), "idempotency-key-required") {
		t.Fatalf("PUT without a key = %d %v", code, got)
	}
	if code, got := policyOf(t, a, "PUT", "pol-bad", `{"maxCostDeviationPercent":201}`); code != 422 || !strings.Contains(asString(got["type"]), "validation-failed") {
		t.Fatalf("PUT out of range = %d %v", code, got)
	}
	code, got := policyOf(t, a, "PUT", "pol-1", `{"crossRegionSurchargeCents":250,"maxCostDeviationPercent":20}`)
	if code != 200 || got["version"] != 1.0 || got["crossRegionSurchargeCents"] != 250.0 || got["maxCostDeviationPercent"] != 20.0 || got["publishedAt"] == nil {
		t.Fatalf("first publish = %d %v", code, got)
	}
	// The same key and body replays the first answer instead of publishing version 2.
	if code, _ := policyOf(t, a, "PUT", "pol-1", `{"crossRegionSurchargeCents":250,"maxCostDeviationPercent":20}`); code != 200 {
		t.Fatalf("replay = %d", code)
	}
	if code, got := policyOf(t, a, "GET", "", ""); code != 200 || got["version"] != 1.0 {
		t.Fatalf("after the replay the current version is still 1: %d %v", code, got)
	}
	if code, got := policyOf(t, a, "PUT", "pol-2", `{"maxCostDeviationPercent":5}`); code != 200 || got["version"] != 2.0 || got["crossRegionSurchargeCents"] != 0.0 {
		t.Fatalf("second publish = %d %v", code, got)
	}
	an := readUntil(t, brokers, topicAnalytics, func(e []outEvent) bool { return countType(e, ceTypePolicy) >= 2 })
	if n := countType(an, ceTypePolicy); n != 2 {
		t.Fatalf("SourcingPolicyPublished published %d times, want 2 (the replay publishes nothing)", n)
	}
	for _, e := range an {
		if e.Type == ceTypePolicy && (e.Subject != "policy" || e.Raw["dataschema"] != "urn:retailnetwork:order-sourcing:analytics:SourcingPolicyPublished:v1" || e.Data["version"] == nil) {
			t.Errorf("policy event = %+v", e)
		}
	}
}

func countType(events []outEvent, typ string) int {
	n := 0
	for _, e := range events {
		if e.Type == typ {
			n++
		}
	}
	return n
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}
