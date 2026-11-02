package outbox

import (
	"errors"
	"strings"
	"testing"
)

// TestSendFailureErrorWording pins the historic "outbox row <id> (<type>): <cause>"
// wording the runbook and the alert queries grep for, and that Unwrap exposes
// the send error to errors.Is.
func TestSendFailureErrorWording(t *testing.T) {
	cause := errors.New("kafka: broker timeout")
	f := &SendFailureError{RowID: 42, Message: Message{EventType: "com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentLocked"}, Attempts: 3, Err: cause}
	want := "outbox row 42 (com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentLocked): kafka: broker timeout"
	if got := f.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(f, cause) {
		t.Fatal("errors.Is must see the send error through Unwrap")
	}
	if got := f.Unwrap(); got == nil || got.Error() != cause.Error() {
		t.Fatalf("Unwrap() = %v, want the send error", got)
	}
	if !strings.Contains(f.Error(), "FulfillmentLocked") {
		t.Fatal("the message type stays greppable in the error")
	}
}
