package main

import (
	"bytes"
	"strings"
	"testing"

	"rocguardd/internal/model"
)

func TestWritePSRowsFormatsTable(t *testing.T) {
	var buf bytes.Buffer
	err := writePSRows(&buf, []model.PSRow{{
		ID:      "res_test",
		GPU:     3,
		User:    "alice",
		Command: "reserved until 2026-07-02T01:00:00Z",
	}})
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"id", "gpu", "user", "command", "res_test", "reserved until"} {
		if !strings.Contains(out, want) {
			t.Fatalf("formatted ps output missing %q: %q", want, out)
		}
	}
}
