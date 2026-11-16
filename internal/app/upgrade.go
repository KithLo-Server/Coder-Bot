package app

import (
	"context"
	"errors"
	"fmt"

	"opencodebot/internal/config"
	"opencodebot/internal/docker"
	"opencodebot/internal/telegram"
)

// CmdUpgrade implements /upgrade.
func (a *App) CmdUpgrade(ctx context.Context, update *telegram.Update, args []string) error {
	if !a.Authorized(update) {
		return nil
	}
	status, err := a.replyMarkdown(ctx, update, "Pulling the latest worker and shell images...")
	if err != nil {
		return err
	}

	workerBefore, _ := a.Docker.ImageID(ctx, config.WorkerImage)
	workerAfter, err := a.Docker.PullImage(ctx, config.WorkerImage)
	if err != nil {
		a.editStatus(ctx, status, fmt.Sprintf("Failed to pull the worker image: `%v`", err))
		return nil
	}
	shellBefore, _ := a.Docker.ImageID(ctx, config.ShellImage)
	shellAfter, err := a.Docker.PullImage(ctx, config.ShellImage)
	if err != nil {
		a.editStatus(ctx, status, fmt.Sprintf("Failed to pull the shell image: `%v`", err))
		return nil
	}
	workerChanged := workerBefore != workerAfter
	shellChanged := shellBefore != shellAfter

	a.editStatus(ctx, status, "Applying image updates to active containers...")

	var workerReplaced, shellReplaced int
	var failed []string
	names := a.RepoKeys()
	for _, name := range names {
		if !a.RepoExists(name) {
			continue
		}
		lockCtx, release, err := a.Locks.Acquire(ctx, name)
		if err != nil {
			failed = append(failed, name)
			continue
		}
		replacedWorker, replacedShell, err := a.upgradeRepoContainers(lockCtx, name, workerAfter, shellAfter, workerChanged, shellChanged)
		if err != nil {
			a.Logger.Printf("Failed to upgrade %s: %v", name, err)
			failed = append(failed, name)
			release()
			continue
		}
		if replacedWorker || replacedShell {
			a.clearRepoPendingPlans(lockCtx, name)
		}
		if replacedWorker {
			workerReplaced++
		}
		if replacedShell {
			shellReplaced++
		}
		release()
	}

	var report docker.PruneReport
	var pruneErr error
	if len(names) > 0 {
		report, pruneErr = a.Docker.PruneSystem(ctx)
	}
	summary := fmt.Sprintf("Upgrade complete.\n**Worker image:** %s\n**Shell image:** %s\n**Containers replaced:** %d worker(s), %d shell(s)",
		changeLabel(workerChanged), changeLabel(shellChanged), workerReplaced, shellReplaced)
	if len(failed) > 0 {
		summary += "\nFailed: " + quoteList(failed)
	}
	if len(names) == 0 {
		summary += "\n**Prune:** skipped (no repositories configured)"
	} else if pruneErr != nil {
		summary += fmt.Sprintf("\n**Prune:** failed: `%v`", pruneErr)
	} else {
		summary += fmt.Sprintf("\n**Pruned:** %d unused image(s), %d container(s), %d network(s), %s bytes reclaimed",
			report.ImagesDeleted, report.ContainersDeleted, report.NetworksDeleted, commaInt(int(report.SpaceReclaimed)))
	}
	a.editStatus(ctx, status, summary)
	return nil
}

func changeLabel(changed bool) string {
	if changed {
		return "updated"
	}
	return "unchanged"
}

func (a *App) upgradeRepoContainers(ctx context.Context, name, workerImage, shellImage string, workerChanged, shellChanged bool) (bool, bool, error) {
	repo, ok := a.Repo(name)
	if !ok {
		return false, false, nil
	}
	replacedWorker, replacedShell := false, false
	if workerChanged {
		replaced, err := a.replaceOutdatedContainer(ctx, repo.ContainerID, workerContainerName(name), workerImage)
		if err != nil {
			return false, false, err
		}
		replacedWorker = replaced
	}
	if shellChanged {
		replaced, err := a.replaceOutdatedContainer(ctx, repo.ShellContainerID, shellContainerName(name), shellImage)
		if err != nil {
			return replacedWorker, false, err
		}
		replacedShell = replaced
	}
	if !replacedWorker && !replacedShell {
		return false, false, nil
	}
	worker, shell, err := a.ensureRepoContainers(ctx, name)
	if err != nil {
		return replacedWorker, replacedShell, err
	}
	if err := a.Store.UpdateRepoContainerIDs(name, worker.ID(), shell.ID()); err != nil {
		return replacedWorker, replacedShell, err
	}
	return replacedWorker, replacedShell, nil
}

func (a *App) replaceOutdatedContainer(ctx context.Context, id, name, imageID string) (bool, error) {
	container, err := a.findContainer(ctx, id, name)
	if err != nil {
		return false, err
	}
	if container == nil {
		return false, nil
	}
	current, err := container.ImageID(ctx)
	if err != nil {
		if errors.Is(err, docker.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	if current == "" || current == imageID {
		return false, nil
	}
	a.RemoveContainer(ctx, container)
	return true, nil
}

func (a *App) findContainer(ctx context.Context, id, name string) (docker.Container, error) {
	if id != "" {
		container, err := a.Docker.GetContainer(ctx, id)
		if err == nil {
			return container, nil
		}
		if !errors.Is(err, docker.ErrNotFound) {
			return nil, err
		}
	}
	container, err := a.Docker.GetContainer(ctx, name)
	if err != nil {
		if errors.Is(err, docker.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return container, nil
}
