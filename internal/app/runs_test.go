package app

import (
	"strings"
	"testing"
	"time"

	"opencodebot/internal/docker"
)

func TestExecuteCancellableDetectsCancel(t *testing.T) {
	env := newTestEnv(t)
	worker := env.addRepo("repo")
	worker.execFunc = func(options docker.ExecOptions) (docker.ExecResult, error) {
		env.app.requestCancel("repo")
		return docker.ExecResult{Output: []byte("partial")}, nil
	}
	_, err := env.app.ExecuteCancellable(env.ctx, worker, "sleep 1", 1, "repo", "/workspace")
	if err != ErrRunCancelled {
		t.Fatalf("err = %v", err)
	}
}

func TestCancelWithoutRunBumpsGeneration(t *testing.T) {
	env := newTestEnv(t)
	env.addRepo("repo")
	env.app.SetActiveContext(1, "repo")
	before := env.app.Generation("repo")
	env.app.CmdCancel(env.ctx, env.message("/cancel", 1), nil)
	if env.app.Generation("repo") != before+1 {
		t.Fatalf("generation = %d", env.app.Generation("repo"))
	}
	if !strings.Contains(env.tg.messages[len(env.tg.messages)-1].Text, "No command is running") {
		t.Fatalf("messages = %+v", env.tg.messages)
	}
}

func TestCancelSignalsTheContainerRunningTheCommand(t *testing.T) {
	env := newTestEnv(t)
	worker := env.addRepo("repo")
	env.app.SetActiveContext(1, "repo")
	shell := env.repoShell("repo")
	runID := env.app.startRun(shell.ID(), 1, "repo")
	signalled := false
	shell.execFunc = func(options docker.ExecOptions) (docker.ExecResult, error) {
		if len(options.Cmd) == 3 && strings.Contains(options.Cmd[2], "OPENCODE_CANCEL_TOKEN") {
			signalled = true
			return docker.ExecResult{ExitCode: 0}, nil
		}
		return docker.ExecResult{}, nil
	}
	env.app.CmdCancel(env.ctx, env.message("/cancel", 1), nil)
	if !signalled {
		t.Fatal("the shell container should have been signalled")
	}
	if worker.restarted != 0 {
		t.Fatal("the worker must not be restarted for a shell run")
	}
	if !strings.Contains(env.tg.messages[len(env.tg.messages)-1].Text, "Cancelled the running command") {
		t.Fatalf("messages = %+v", env.tg.messages)
	}
	env.app.finishRun("repo", runID)
}

func TestCancelKillsRunningProcesses(t *testing.T) {
	env := newTestEnv(t)
	worker := env.addRepo("repo")
	env.app.SetActiveContext(1, "repo")
	runID := env.app.startRun(worker.ID(), 1, "repo")
	worker.execFunc = func(options docker.ExecOptions) (docker.ExecResult, error) {
		if len(options.Cmd) == 3 && strings.Contains(options.Cmd[2], "OPENCODE_CANCEL_TOKEN") {
			return docker.ExecResult{ExitCode: 0}, nil
		}
		return docker.ExecResult{}, nil
	}
	env.app.CmdCancel(env.ctx, env.message("/cancel", 1), nil)
	if !env.app.runStillActive("repo", runID) {
		t.Fatal("run should remain until the worker thread finishes")
	}
	if !strings.Contains(env.tg.messages[len(env.tg.messages)-1].Text, "Cancelled the running command") {
		t.Fatalf("messages = %+v", env.tg.messages)
	}
	env.app.finishRun("repo", runID)
}

func TestCancelFallsBackToRestart(t *testing.T) {
	env := newTestEnv(t)
	worker := env.addRepo("repo")
	env.app.SetActiveContext(1, "repo")
	runID := env.app.startRun(worker.ID(), 1, "repo")
	worker.execFunc = func(options docker.ExecOptions) (docker.ExecResult, error) {
		return docker.ExecResult{ExitCode: 3}, nil
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		env.app.finishRun("repo", runID)
	}()
	env.app.CmdCancel(env.ctx, env.message("/cancel", 1), nil)
	if worker.restarted != 0 {
		t.Fatal("should not restart when the run finishes shortly")
	}
	if !strings.Contains(env.tg.messages[len(env.tg.messages)-1].Text, "Cancelled the running command") {
		t.Fatalf("messages = %+v", env.tg.messages)
	}
}

func TestCancelRestartsUnresponsiveWorker(t *testing.T) {
	env := newTestEnv(t)
	worker := env.addRepo("repo")
	env.app.SetActiveContext(1, "repo")
	env.app.startRun(worker.ID(), 1, "repo")
	worker.execFunc = func(options docker.ExecOptions) (docker.ExecResult, error) {
		return docker.ExecResult{ExitCode: 3}, nil
	}
	env.app.CmdCancel(env.ctx, env.message("/cancel", 1), nil)
	if worker.restarted != 1 {
		t.Fatalf("restarted = %d", worker.restarted)
	}
	if !strings.Contains(env.tg.messages[len(env.tg.messages)-1].Text, "Forced the container") {
		t.Fatalf("messages = %+v", env.tg.messages)
	}
}

func TestCancelSignalsBaseContainerWithoutRestartingIt(t *testing.T) {
	env := newTestEnv(t)
	env.addRepo("repo")
	env.app.SetActiveContext(1, "repo")
	runID := env.app.startRun(env.base.ID(), 1, "repo")
	signalled := false
	env.base.execFunc = func(options docker.ExecOptions) (docker.ExecResult, error) {
		if len(options.Cmd) == 3 && strings.Contains(options.Cmd[2], "OPENCODE_CANCEL_TOKEN") {
			signalled = true
			return docker.ExecResult{ExitCode: 0}, nil
		}
		return docker.ExecResult{}, nil
	}
	env.app.CmdCancel(env.ctx, env.message("/cancel", 1), nil)
	if !signalled {
		t.Fatal("the base container should have been signalled")
	}
	if env.base.restarted != 0 {
		t.Fatal("the base container must never be restarted")
	}
	env.app.finishRun("repo", runID)
}
