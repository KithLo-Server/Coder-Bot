package app

import (
	"strings"
	"testing"

	"opencodebot/internal/docker"
)

func gitResultFor(options docker.ExecOptions) docker.ExecResult {
	script := ""
	if len(options.Cmd) == 3 {
		script = options.Cmd[2]
	}
	switch {
	case strings.Contains(script, "rev-parse --abbrev-ref"):
		return docker.ExecResult{ExitCode: 0, Output: []byte("origin/main\n")}
	case strings.Contains(script, "rev-list"):
		return docker.ExecResult{ExitCode: 0, Output: []byte("0\n")}
	case strings.Contains(script, " pull --rebase"):
		return docker.ExecResult{ExitCode: 0}
	case strings.Contains(script, " push"):
		return docker.ExecResult{ExitCode: 0}
	case strings.Contains(script, " fetch"):
		return docker.ExecResult{ExitCode: 0}
	}
	return docker.ExecResult{}
}

func TestPushWithRebaseStatuses(t *testing.T) {
	env := newTestEnv(t)
	shell := env.shell
	shell.execFunc = func(options docker.ExecOptions) (docker.ExecResult, error) {
		return gitResultFor(options), nil
	}
	if status, _ := pushWithRebase(env.ctx, shell, "/workspace/repo"); status != "ok" {
		t.Fatalf("status = %q", status)
	}

	shell.execFunc = func(options docker.ExecOptions) (docker.ExecResult, error) {
		return docker.ExecResult{ExitCode: 1}, nil
	}
	if status, _ := pushWithRebase(env.ctx, shell, "/workspace/repo"); status != "no-upstream" {
		t.Fatalf("status = %q", status)
	}

	shell.execFunc = func(options docker.ExecOptions) (docker.ExecResult, error) {
		script := options.Cmd[2]
		switch {
		case strings.Contains(script, "rev-parse --abbrev-ref"):
			return docker.ExecResult{ExitCode: 0, Output: []byte("origin/main\n")}, nil
		case strings.Contains(script, " fetch"):
			return docker.ExecResult{ExitCode: 0}, nil
		case strings.Contains(script, "rev-list"):
			return docker.ExecResult{ExitCode: 0, Output: []byte("2\n")}, nil
		case strings.Contains(script, " pull --rebase"):
			return docker.ExecResult{ExitCode: 1, Output: []byte("conflict")}, nil
		case strings.Contains(script, "rebase-merge"):
			return docker.ExecResult{ExitCode: 0}, nil
		}
		return docker.ExecResult{}, nil
	}
	if status, _ := pushWithRebase(env.ctx, shell, "/workspace/repo"); status != "conflict" {
		t.Fatalf("status = %q", status)
	}
}

func TestPullConflictAbortsRebase(t *testing.T) {
	env := newTestEnv(t)
	var sawAbort bool
	env.shell.execFunc = func(options docker.ExecOptions) (docker.ExecResult, error) {
		script := options.Cmd[2]
		switch {
		case strings.Contains(script, " pull --rebase"):
			return docker.ExecResult{ExitCode: 1}, nil
		case strings.Contains(script, "rebase-merge"):
			return docker.ExecResult{ExitCode: 0}, nil
		case strings.Contains(script, " rebase --abort"):
			sawAbort = true
		}
		return docker.ExecResult{}, nil
	}
	pulled, conflicted, _ := pullRebase(env.ctx, env.shell, "/workspace/repo")
	if pulled || !conflicted || !sawAbort {
		t.Fatalf("pulled=%v conflicted=%v abort=%v", pulled, conflicted, sawAbort)
	}
}

func TestPullAndPushCommands(t *testing.T) {
	env := newTestEnv(t)
	env.addRepo("repo")
	env.app.SetActiveContext(1, "repo")
	env.shell.execFunc = func(options docker.ExecOptions) (docker.ExecResult, error) {
		return gitResultFor(options), nil
	}
	if err := env.app.CmdPull(env.ctx, env.message("/pull", 1), nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(env.tg.edits[len(env.tg.edits)-1].Text, "Pulled `repo`") {
		t.Fatalf("edits = %+v", env.tg.edits)
	}
	if err := env.app.CmdPush(env.ctx, env.message("/push", 1), nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(env.tg.edits[len(env.tg.edits)-1].Text, "Pushed `repo`") {
		t.Fatalf("edits = %+v", env.tg.edits)
	}
}
