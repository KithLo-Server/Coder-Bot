package app

import (
	"strings"
	"testing"

	"opencodebot/internal/docker"
	"opencodebot/internal/state"
)

func TestSubstitutePlaceholders(t *testing.T) {
	got := SubstitutePlaceholders("/sh make %1 && echo %% && echo %2 && echo %9", []string{"release", "100%"})
	if got != "/sh make release && echo % && echo 100% && echo %9" {
		t.Fatalf("got %q", got)
	}
}

func TestSplitBotCommandAndExecTarget(t *testing.T) {
	name, args, ok := splitBotCommand("/sh git status --short")
	if !ok || name != "sh" || strings.Join(args, "|") != "git|status|--short" {
		t.Fatalf("split = %q %v %v", name, args, ok)
	}
	if _, _, ok := splitBotCommand("git status"); ok {
		t.Fatal("plain line must not be a bot command")
	}
	sets := map[string]*state.CommandSet{"deploy two": {Name: "deploy two"}, "deploy": {Name: "deploy"}}
	if name, args := splitExecTarget([]string{"deploy", "release"}, sets); name != "deploy" || len(args) != 1 || args[0] != "release" {
		t.Fatalf("split target = %q %v", name, args)
	}
	if name, args := splitExecTarget([]string{"deploy", "two"}, sets); name != "deploy two" || len(args) != 0 {
		t.Fatalf("full-name target = %q %v", name, args)
	}
}

func TestCommandSetFlowAndExec(t *testing.T) {
	env := newTestEnv(t)
	chatID := int64(1)
	env.app.SetActiveContext(chatID, "repo")
	env.addRepo("repo")

	update := env.message("/cmd deploy", chatID)
	if err := env.app.CmdCmd(env.ctx, update, []string{"deploy"}); err != nil {
		t.Fatal(err)
	}
	if pending := env.app.GetPendingCommand(chatID); pending == nil || pending.Name != "deploy" {
		t.Fatalf("pending = %+v", pending)
	}
	if len(env.tg.messages) == 0 || env.tg.messages[len(env.tg.messages)-1].Options.Keyboard == nil {
		t.Fatal("expected collection keyboard")
	}

	for _, line := range []string{"/sh make %1", "git status"} {
		handled, err := env.app.HandleCommandEntryText(env.ctx, env.message(line, chatID))
		if err != nil || !handled {
			t.Fatalf("capture %q handled=%v err=%v", line, handled, err)
		}
	}
	if err := env.app.HandleCommandCallback(env.ctx, env.callback("cmdset:done", chatID)); err != nil {
		t.Fatal(err)
	}
	set, ok := env.app.CommandSet(chatID, "deploy")
	if !ok || len(set.Commands) != 2 {
		t.Fatalf("set = %+v", set)
	}
	if env.app.GetPendingCommand(chatID) != nil {
		t.Fatal("collection should be finished")
	}

	var executed []string
	record := func(options docker.ExecOptions) (docker.ExecResult, error) {
		executed = append(executed, options.Cmd[len(options.Cmd)-1])
		return docker.ExecResult{ExitCode: 0, Output: []byte("ok")}, nil
	}
	env.base.execFunc = record
	env.repoShell("repo").execFunc = record
	if err := env.app.CmdExec(env.ctx, env.message("/exec deploy release", chatID), []string{"deploy", "release"}); err != nil {
		t.Fatal(err)
	}
	if len(executed) != 2 {
		t.Fatalf("executed = %v", executed)
	}
	if !strings.Contains(executed[0], "make release") || !strings.Contains(executed[1], "git status") {
		t.Fatalf("executed = %v", executed)
	}
}

func TestCommandSetEditDeleteCancel(t *testing.T) {
	env := newTestEnv(t)
	chatID := int64(1)
	env.app.SaveCommandSet(chatID, &state.CommandSet{Name: "x", Commands: []string{"one", "two"}})

	env.app.HandleCommandCallback(env.ctx, env.callback("cmdset:edit:x", chatID))
	pending := env.app.GetPendingCommand(chatID)
	if pending == nil || !pending.Editing || len(pending.Commands) != 0 {
		t.Fatalf("edit pending = %+v", pending)
	}
	env.app.HandleCommandEntryText(env.ctx, env.message("three", chatID))
	env.app.HandleCommandCallback(env.ctx, env.callback("cmdset:done", chatID))
	set, _ := env.app.CommandSet(chatID, "x")
	if strings.Join(set.Commands, ",") != "three" {
		t.Fatalf("edited set = %+v", set)
	}

	env.app.HandleCommandCallback(env.ctx, env.callback("cmdset:delete:x", chatID))
	if _, ok := env.app.CommandSet(chatID, "x"); ok {
		t.Fatal("set should be deleted")
	}

	env.app.CmdCmd(env.ctx, env.message("/cmd y", chatID), []string{"y"})
	env.app.HandleCommandEntryText(env.ctx, env.message("line", chatID))
	env.app.HandleCommandCallback(env.ctx, env.callback("cmdset:cancel", chatID))
	if env.app.GetPendingCommand(chatID) != nil {
		t.Fatal("pending should be cancelled")
	}
	if _, ok := env.app.CommandSet(chatID, "y"); ok {
		t.Fatal("cancelled set must not be saved")
	}
}
