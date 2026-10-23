package kafka_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/claudioed/order-sourcing/internal/adapters/outbound/kafka"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

var update = flag.Bool("update", false, "rewrite the golden CloudEvents under testdata/golden")

// TestGoldenCloudEvents pins the exact JSON of every published event, byte for byte (indented for review),
// so that a renamed field, a changed type or a dropped attribute fails here before it reaches a consumer.
func TestGoldenCloudEvents(t *testing.T) {
	enc := kafka.Encoder{NewID: func() string { return "11111111-1111-4111-8111-111111111111" }}
	for _, ev := range allEvents() {
		t.Run(ev.EventName(), func(t *testing.T) {
			got := encodeIndented(t, enc, ev)
			path := filepath.Join("testdata", "golden", ev.EventName()+".json")
			if *update {
				writeGolden(t, path, got)
			}
			want, err := os.ReadFile(path) //nolint:gosec // fixed testdata path
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(want, got) {
				t.Errorf("%s differs from its golden file\n got:\n%s\nwant:\n%s", ev.EventName(), got, want)
			}
		})
	}
}

func encodeIndented(t *testing.T, enc kafka.Encoder, ev shared.Event) []byte {
	t.Helper()
	m, err := enc.Encode(ev)
	if err != nil {
		t.Fatal(err)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, m.Value, "", "  "); err != nil {
		t.Fatal(err)
	}
	pretty.WriteByte('\n')
	return pretty.Bytes()
}

func writeGolden(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}
