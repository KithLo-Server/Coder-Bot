package app

import (
	"errors"
	"strings"
	"testing"

	"opencodebot/internal/config"
	"opencodebot/internal/docker"
)

func TestUpgradeReplacesOnlyChangedContainers(t *testing.T) {
	env := newTestEnv(t)
	env.docker.images[config.WorkerImage] = "worker-old"
	env.docker.images[config.ShellImage] = "shell-current"
	env.addRepo("repo")
	env.docker.pullImages[config.WorkerImage] = "worker-new"
	env.docker.pullImages[config.ShellImage] = "shell-current"
	env.docker.prune = docker.PruneReport{ImagesDeleted: 2, SpaceReclaimed: 2048}

	oldWorker := env.docker.containers[workerContainerName("repo")]
	oldShell := env.docker.containers[shellContainerName("repo")]
	env.app.SetSession(1, "repo", "session-1")

	if err := env.app.CmdUpgrade(env.ctx, env.message("/upgrade", 1), nil); err != nil {
		t.Fatalf("CmdUpgrade returned an error: %v", err)
	}

	oldWorker.mu.Lock()
	removedWorker := oldWorker.removed
	oldWorker.mu.Unlock()
	if removedWorker == 0 {
		t.Fatal("expected the outdated worker to be removed")
	}

	oldShell.mu.Lock()
	removedShell := oldShell.removed
	oldShell.mu.Unlock()
	if removedShell != 0 {
		t.Fatal("expected the up-to-date shell to be kept")
	}

	repo, ok := env.app.Repo("repo")
	if !ok {
		t.Fatal("expected the repository state to survive the upgrade")
	}
	if repo.ContainerID == oldWorker.id {
		t.Fatal("expected the worker container id to be updated")
	}
	if repo.ShellContainerID != oldShell.id {
		t.Fatal("expected the shell container id to stay the same")
	}
	if got := env.app.Session(1, "repo"); got != "session-1" {
		t.Fatalf("expected session to be retained, got %q", got)
	}
	if len(env.docker.runCalls) != 1 {
		t.Fatalf("expected one replacement container, got %d", len(env.docker.runCalls))
	}
	if env.docker.runCalls[0].Image != config.WorkerImage {
		t.Fatalf("expected the worker image to be used, got %q", env.docker.runCalls[0].Image)
	}
	if env.docker.pruneCalls != 1 {
		t.Fatalf("expected one prune call, got %d", env.docker.pruneCalls)
	}
}

func TestUpgradeKeepsContainersWhenImagesAreUnchanged(t *testing.T) {
	env := newTestEnv(t)
	env.docker.images[config.WorkerImage] = "worker-current"
	env.docker.images[config.ShellImage] = "shell-current"
	env.addRepo("repo")

	if err := env.app.CmdUpgrade(env.ctx, env.message("/upgrade", 1), nil); err != nil {
		t.Fatalf("CmdUpgrade returned an error: %v", err)
	}

	if len(env.docker.runCalls) != 0 {
		t.Fatalf("expected no replacement containers, got %d", len(env.docker.runCalls))
	}
	for _, name := range []string{workerContainerName("repo"), shellContainerName("repo")} {
		container := env.docker.containers[name]
		container.mu.Lock()
		removed := container.removed
		container.mu.Unlock()
		if removed != 0 {
			t.Fatalf("expected %s to be kept", name)
		}
	}
	if env.docker.pruneCalls != 1 {
		t.Fatalf("expected one prune call, got %d", env.docker.pruneCalls)
	}
}

func TestUpgradeStopsOnPullFailure(t *testing.T) {
	env := newTestEnv(t)
	env.docker.pullErr = errors.New("registry unavailable")
	env.addRepo("repo")

	if err := env.app.CmdUpgrade(env.ctx, env.message("/upgrade", 1), nil); err != nil {
		t.Fatalf("CmdUpgrade returned an error: %v", err)
	}

	if env.docker.pruneCalls != 0 {
		t.Fatalf("expected no prune after a failed pull, got %d", env.docker.pruneCalls)
	}
	if len(env.docker.runCalls) != 0 {
		t.Fatalf("expected no replacement containers, got %d", len(env.docker.runCalls))
	}
	if len(env.tg.edits) == 0 || !strings.Contains(env.tg.edits[len(env.tg.edits)-1].Text, "Failed to pull") {
		t.Fatalf("expected a pull failure message, got %#v", env.tg.edits)
	}
}

func TestUpgradeRequiresAuthorization(t *testing.T) {
	env := newTestEnv(t)
	env.app.Cfg.AllowedUserIDs = map[string]bool{"999": true}

	if err := env.app.CmdUpgrade(env.ctx, env.message("/upgrade", 1), nil); err != nil {
		t.Fatalf("CmdUpgrade returned an error: %v", err)
	}

	if len(env.docker.pullCalls) != 0 {
		t.Fatalf("expected no pulls for an unauthorized user, got %v", env.docker.pullCalls)
	}
	if env.docker.pruneCalls != 0 {
		t.Fatalf("expected no prune for an unauthorized user, got %d", env.docker.pruneCalls)
	}
}
