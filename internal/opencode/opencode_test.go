package opencode

import (
	"strings"
	"testing"
)

func TestParseEventsAndResponse(t *testing.T) {
	raw := strings.Join([]string{
		`not json`,
		`{"type":"text","part":{"text":"first"},"sessionID":"ses_1"}`,
		`{"type":"text","part":{"text":"second"}}`,
		`{"type":"error","error":{"data":{"message":"boom"}}}`,
	}, "\n")
	events := ParseEvents(raw)
	if events.SessionID != "ses_1" || len(events.Texts) != 2 || len(events.Errors) != 1 {
		t.Fatalf("unexpected events: %+v", events)
	}
	if Response(raw) != "first\nsecond" {
		t.Fatalf("response = %q", Response(raw))
	}
	if FailedWithoutText(raw) {
		t.Fatal("failed without text should be false when text exists")
	}
}

func TestResponseFallsBack(t *testing.T) {
	if Response("plain output") != "plain output" {
		t.Fatal("expected raw output")
	}
	raw := `{"type":"error","error":{"data":{"message":"boom"}}}`
	if Response(raw) != "⚠️ Error: boom" {
		t.Fatalf("response = %q", Response(raw))
	}
	if !FailedWithoutText(raw) {
		t.Fatal("expected failure without text")
	}
}

func TestBuildCommand(t *testing.T) {
	command := BuildCommand("fix $HOME and `x`", "build", "ses 1", "high", "", "deepseek/flash")
	want := `opencode run --format json --session 'ses 1' --agent build --model "deepseek/flash" --variant high 'fix $HOME and ` + "`x`" + `'`
	if command != want {
		t.Fatalf("command = %s\nwant    = %s", command, want)
	}
	command = BuildCommand("it's", "plan", "", "off", "m", "default")
	if strings.Contains(command, "--session") || strings.Contains(command, "--variant") {
		t.Fatalf("unexpected flags: %s", command)
	}
	if !strings.HasSuffix(command, `'it'"'"'s'`) {
		t.Fatalf("quote mismatch: %s", command)
	}
}

func TestFormatUntrustedOutput(t *testing.T) {
	if got := FormatUntrustedOutput("a ``` b", 0); !strings.HasPrefix(got, "````\n") {
		t.Fatalf("fence = %q", got)
	}
	if got := FormatUntrustedOutput("", 0); got != "```\n(no output)\n```" {
		t.Fatalf("empty = %q", got)
	}
}
