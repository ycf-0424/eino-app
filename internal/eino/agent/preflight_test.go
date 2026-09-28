package agent

import (
	"context"
	"testing"
)

func TestAppendPreflightContextPreservesIndependentEvidence(t *testing.T) {
	chat := &ChatAgent{}
	chat.AppendPreflightContext("<attachment>file evidence</attachment>")
	chat.AppendPreflightContext("<knowledge>document evidence</knowledge>")
	chat.AppendPreflightContext("  ")
	want := "<attachment>file evidence</attachment>\n\n<knowledge>document evidence</knowledge>"
	if chat.preflight != want {
		t.Fatalf("preflight context = %q, want %q", chat.preflight, want)
	}
}

func TestEnsureWriteNoteToolAddsOnceForLegacyAgentConstructors(t *testing.T) {
	tools, err := ensureWriteNoteTool(context.Background(), nil)
	if err != nil || len(tools) != 1 {
		t.Fatalf("tools=%v err=%v, want one default write_note tool", tools, err)
	}
	info, err := tools[0].Info(context.Background())
	if err != nil || info.Name != "write_note" {
		t.Fatalf("tool info=%+v err=%v", info, err)
	}
	tools, err = ensureWriteNoteTool(context.Background(), tools)
	if err != nil || len(tools) != 1 {
		t.Fatalf("duplicate write_note registration: tools=%v err=%v", tools, err)
	}
}
