package rag

import (
	"path/filepath"
	"testing"
)

func TestTraceRestoresRecentQuestionsAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "traces.jsonl")
	first := NewTracer(2, path)
	first.Add(Trace{Question: "第一问"})
	first.Add(Trace{Question: "第二问"})
	first.Add(Trace{Question: "第三问"})
	first.Close()
	next := NewTracer(2, path)
	defer next.Close()
	got := next.List(10)
	if len(got) != 2 || got[0].Question != "第三问" || got[1].Question != "第二问" {
		t.Fatalf("unexpected restored traces: %+v", got)
	}
}
