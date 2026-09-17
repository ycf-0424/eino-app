package tool

import (
	"context"
	"strings"
	"testing"
)

func TestTimeTool(t *testing.T) {
	result, err := NewTimeTool().InvokableRun(context.Background(), `{"timezone":"UTC"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "UTC") {
		t.Fatalf("result = %q, want UTC timestamp", result)
	}
}

func TestTimeToolRejectsInvalidTimezone(t *testing.T) {
	if _, err := NewTimeTool().InvokableRun(context.Background(), `{"timezone":"not/a-zone"}`); err == nil {
		t.Fatal("expected invalid timezone error")
	}
}

func TestValidateNoteArguments(t *testing.T) {
	if err := ValidateNoteArguments(`{"name":"release-note","content":"ok"}`); err != nil {
		t.Fatal(err)
	}
	for _, args := range []string{`{"name":"","content":"ok"}`, `{"name":"bad/name","content":"ok"}`, `{"name":"ok","content":""}`} {
		if err := ValidateNoteArguments(args); err == nil {
			t.Fatalf("expected invalid note arguments for %s", args)
		}
	}
}
