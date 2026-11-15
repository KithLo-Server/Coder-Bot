package app

import (
	"bytes"
	"strings"
	"testing"

	"opencodebot/internal/config"
	"opencodebot/internal/telegram"
)

func TestSendResponseEmpty(t *testing.T) {
	env := newTestEnv(t)
	sent, err := env.app.SendResponse(env.ctx, 1, 0, "  ", true, "x", nil)
	if err != nil || sent == nil || env.tg.messages[0].Text != "(Empty response)" {
		t.Fatalf("sent=%v err=%v messages=%+v", sent, err, env.tg.messages)
	}
}

func TestSendResponseMarkdownFallback(t *testing.T) {
	env := newTestEnv(t)
	env.tg.markdownErr = true
	_, err := env.app.SendResponse(env.ctx, 1, 7, "**hello**", true, "x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(env.tg.messages) != 1 {
		t.Fatalf("messages = %+v", env.tg.messages)
	}
	if env.tg.messages[0].Options.Markdown || env.tg.messages[0].Text != "**hello**" {
		t.Fatalf("fallback = %+v", env.tg.messages[0])
	}
}

func TestSendResponseOversizedDocument(t *testing.T) {
	env := newTestEnv(t)
	text := strings.Repeat("x", config.MaxMessageLength+1)
	_, err := env.app.SendResponse(env.ctx, 1, 7, text, true, "plan", telegram.Keyboard{{{Text: "b", Data: "d"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(env.tg.documents) != 1 || string(env.tg.documentRaw) != text {
		t.Fatalf("documents = %+v", env.tg.documents)
	}
	if !strings.Contains(env.tg.documents[0].Text, "plan.md") || len(env.tg.documents[0].Options.Keyboard) != 0 {
		t.Fatalf("caption = %+v", env.tg.documents[0])
	}
}

func TestSendResponsePlainWrapsCodeBlock(t *testing.T) {
	env := newTestEnv(t)
	_, err := env.app.SendResponse(env.ctx, 1, 0, "out", false, "sh_output", nil)
	if err != nil {
		t.Fatal(err)
	}
	if env.tg.messages[0].Text != "```\nout\n```" || !env.tg.messages[0].Options.Markdown {
		t.Fatalf("message = %+v", env.tg.messages[0])
	}
}

func TestAuthorization(t *testing.T) {
	env := newTestEnv(t)
	env.app.Cfg.AllowedUserIDs = map[string]bool{"5": true}
	if env.app.Authorized(&telegram.Update{UserID: 1}) {
		t.Fatal("user 1 should not be authorized")
	}
	if !env.app.Authorized(&telegram.Update{UserID: 5}) {
		t.Fatal("user 5 should be authorized")
	}
	env.app.Cfg.AllowedUserIDs = map[string]bool{}
	if !env.app.Authorized(&telegram.Update{UserID: 99}) {
		t.Fatal("empty allow-list should allow everyone")
	}
}

func TestContainerPathHelpers(t *testing.T) {
	cases := map[string]string{
		"git@github.com:owner/repo.git": "https://github.com/owner/repo",
		"https://host/owner/repo.git/":  "https://host/owner/repo",
		"https://host/owner/repo/":      "https://host/owner/repo",
	}
	for input, want := range cases {
		if got := NormalizeRepoURL(input); got != want {
			t.Fatalf("NormalizeRepoURL(%q) = %q, want %q", input, got, want)
		}
	}
	if got := ResolveContainerPath("a/../b", "repo"); got != "/workspace/repo/b" {
		t.Fatalf("resolved = %q", got)
	}
	if got := ResolveContainerPath("/tmp/x", "repo"); got != "/tmp/x" {
		t.Fatalf("absolute = %q", got)
	}
}

func TestReadDemuxedFramedAndRaw(t *testing.T) {
	payload := []byte("hello")
	framed := append([]byte{1, 0, 0, 0, 0, 0, 0, byte(len(payload))}, payload...)
	got, err := readDemuxed(bytes.NewReader(framed), 100)
	if err != nil || string(got) != "hello" {
		t.Fatalf("framed = %q err=%v", got, err)
	}
	got, err = readDemuxed(strings.NewReader("raw"), 100)
	if err != nil || string(got) != "raw" {
		t.Fatalf("raw = %q err=%v", got, err)
	}
	if _, err := readDemuxed(strings.NewReader("too long"), 2); err == nil {
		t.Fatal("expected limit error")
	}

	large := strings.Repeat("a", 200*1024)
	got, err = readDemuxed(strings.NewReader(large), 300*1024)
	if err != nil || string(got) != large {
		t.Fatalf("large raw read failed: len=%d err=%v", len(got), err)
	}

	var multi bytes.Buffer
	for _, part := range []string{"one", "two"} {
		multi.Write([]byte{1, 0, 0, 0, 0, 0, 0, byte(len(part))})
		multi.WriteString(part)
	}
	got, err = readDemuxed(&multi, 100)
	if err != nil || string(got) != "onetwo" {
		t.Fatalf("multi frame = %q err=%v", got, err)
	}
}
