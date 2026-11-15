package app

import (
	"strings"
	"testing"

	"opencodebot/internal/docker"
	"opencodebot/internal/telegram"
)

func TestRouterDispatchesCommandAndPlainText(t *testing.T) {
	env := newTestEnv(t)
	env.addRepo("repo")
	env.app.SetActiveContext(1, "repo")
	env.repoShell("repo").execFunc = func(options docker.ExecOptions) (docker.ExecResult, error) {
		return docker.ExecResult{ExitCode: 0, Output: []byte("shell output")}, nil
	}
	env.app.Handle(env.ctx, env.message("/sh echo hi", 1))
	if len(env.tg.messages) == 0 || !strings.Contains(env.tg.messages[len(env.tg.messages)-1].Text, "shell output") {
		t.Fatalf("messages = %+v", env.tg.messages)
	}
	env.app.Handle(env.ctx, env.message("/info", 1))
	if !strings.Contains(env.tg.messages[len(env.tg.messages)-1].Text, "Active Context") {
		t.Fatalf("info = %+v", env.tg.messages)
	}
}

func TestRouterCaptureBlocksCommandExecution(t *testing.T) {
	env := newTestEnv(t)
	env.addRepo("repo")
	env.app.SetActiveContext(1, "repo")
	executed := false
	env.base.execFunc = func(options docker.ExecOptions) (docker.ExecResult, error) {
		executed = true
		return docker.ExecResult{}, nil
	}
	env.app.CmdCmd(env.ctx, env.message("/cmd set", 1), []string{"set"})
	env.app.Handle(env.ctx, env.message("/sh echo saved", 1))
	if executed {
		t.Fatal("command must be captured instead of executed")
	}
	pending := env.app.GetPendingCommand(1)
	if pending == nil || len(pending.Commands) != 1 || pending.Commands[0] != "/sh echo saved" {
		t.Fatalf("pending = %+v", pending)
	}
}

func TestRouterCaptionULStopsPhotoHandling(t *testing.T) {
	env := newTestEnv(t)
	env.addRepo("repo")
	env.app.SetActiveContext(1, "repo")
	env.tg.files["doc"] = []byte("data")
	env.base.execFunc = func(options docker.ExecOptions) (docker.ExecResult, error) {
		if describeScript(options) {
			return docker.ExecResult{ExitCode: 1}, nil
		}
		return docker.ExecResult{}, nil
	}
	update := &telegram.Update{ChatID: 1, UserID: 1, Message: &telegram.Message{
		ID: 7, ChatID: 1, Caption: "/ul notes.txt",
		Document: &telegram.Attachment{FileID: "doc", FileName: "notes.txt"},
		Photo:    []telegram.PhotoSize{{FileID: "doc"}},
	}}
	env.app.Handle(env.ctx, update)
	if len(env.base.putPaths) != 1 {
		t.Fatalf("expected upload, putPaths = %+v", env.base.putPaths)
	}
}

func TestRouterCallbackDispatch(t *testing.T) {
	env := newTestEnv(t)
	env.addRepo("repo")
	env.app.SetActiveContext(1, "repo")
	env.app.RegisterPendingPlan(1, 42, "tok")
	env.app.Handle(env.ctx, env.callback("planexec:missing", 1))
	if len(env.tg.answered) == 0 || !strings.Contains(env.tg.answered[len(env.tg.answered)-1], "no longer pending") {
		t.Fatalf("answers = %+v", env.tg.answered)
	}
}
