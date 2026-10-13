package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestNewJSON(t *testing.T) {
	var buf bytes.Buffer
	New(&buf, slog.LevelInfo, "json").Info("hello", "k", "v")
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("not json: %v: %s", err, buf.String())
	}
	if m["msg"] != "hello" || m["k"] != "v" {
		t.Errorf("got %v", m)
	}
}

func TestNewTextAndLevel(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, slog.LevelWarn, "text")
	l.Info("hidden")
	l.Warn("shown")
	out := buf.String()
	if strings.Contains(out, "hidden") || !strings.Contains(out, "msg=shown") {
		t.Errorf("got %q", out)
	}
}
