package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"path"
	"regexp"
	"strings"

	"opencodebot/internal/config"
	"opencodebot/internal/docker"
	"opencodebot/internal/opencode"
	"opencodebot/internal/state"
	"opencodebot/internal/telegram"
)

func messageTarget(update *telegram.Update) (int64, int) {
	if update == nil {
		return 0, 0
	}
	if update.Message != nil {
		return update.ChatID, update.Message.ID
	}
	if update.Callback != nil && update.Callback.Message != nil {
		return update.ChatID, update.Callback.Message.ID
	}
	return update.ChatID, 0
}

func (a *App) replyMarkdown(ctx context.Context, update *telegram.Update, text string) (*telegram.Message, error) {
	chatID, replyTo := messageTarget(update)
	return a.Telegram.SendMessage(ctx, chatID, text, telegram.SendOptions{Markdown: true, ReplyTo: replyTo})
}

func (a *App) editStatus(ctx context.Context, status *telegram.Message, text string) {
	if status == nil {
		return
	}
	if err := a.Telegram.EditMessageText(ctx, status.ChatID, status.ID, text, telegram.SendOptions{Markdown: true}); err != nil {
		a.Logger.Printf("Could not edit status message: %v", err)
	}
}

// RemovePendingPlanButton removes the chat's pending plan keyboard, if any.
func (a *App) RemovePendingPlanButton(ctx context.Context, chatID int64) {
	if messageID, ok := a.TakePendingPlanMessage(chatID); ok {
		if err := a.Telegram.EditReplyMarkup(ctx, chatID, messageID, nil); err != nil {
			a.Logger.Printf("Could not remove the plan button from %d: %v", messageID, err)
		}
	}
}

func (a *App) removeRepoContainers(ctx context.Context, name string, repo *state.Repo) {
	references := []string{workerContainerName(name), shellContainerName(name)}
	if repo != nil {
		references = append(references, repo.ContainerID, repo.ShellContainerID)
	}
	seenReferences := make(map[string]bool)
	seenIDs := make(map[string]bool)
	for _, reference := range references {
		if reference == "" || seenReferences[reference] {
			continue
		}
		seenReferences[reference] = true
		container, err := a.Docker.GetContainer(ctx, reference)
		if err != nil {
			continue
		}
		if seenIDs[container.ID()] {
			continue
		}
		seenIDs[container.ID()] = true
		a.RemoveContainer(ctx, container)
	}
}

func (a *App) resetRepoSessions(ctx context.Context, name string) {
	a.ForgetSessionsForRepo(name)
	for chatID, activeName := range a.ActiveContextCopy() {
		if activeName == name {
			a.RemovePendingPlanButton(ctx, chatID)
		}
	}
}

func (a *App) removeRepoFromMounts(name string) {
	for _, repoName := range a.RepoKeys() {
		repo, ok := a.Repo(repoName)
		if !ok || len(repo.Mounts) == 0 {
			continue
		}
		changed := false
		var mounts []string
		for _, mount := range repo.Mounts {
			if mount == name {
				changed = true
				continue
			}
			mounts = append(mounts, mount)
		}
		if changed {
			repo.Mounts = mounts
			a.SetRepo(repoName, repo)
		}
	}
}

func (a *App) forgetRepo(ctx context.Context, name string) {
	a.resetRepoSessions(ctx, name)
	a.removeRepoFromMounts(name)
	a.DeleteRepo(name)
	for chatID, activeName := range a.ActiveContextCopy() {
		if activeName == name {
			a.ClearActiveContext(chatID)
		}
	}
	a.Save()
}

func (a *App) repoVolumes(name string) []docker.Volume {
	volumes := []docker.Volume{
		{
			Source: a.repoBasePath(name),
			Target: config.WorkspaceDir,
			Mode:   "rw",
		},
		{
			Source: a.Cfg.UploadsDir,
			Target: a.Cfg.UploadsDir,
			Mode:   "ro",
		},
	}
	repo, hasRepo := a.Repo(name)
	if !hasRepo {
		return volumes
	}
	for _, mount := range repo.Mounts {
		volumes = append(volumes, docker.Volume{
			Source: path.Join(a.Cfg.ReposDir, mount),
			Target: config.MountDir + "/" + mount,
			Mode:   "rw",
		})
	}
	return volumes
}

func (a *App) createWorkerContainer(ctx context.Context, name string) (docker.Container, error) {
	return a.Docker.RunContainer(ctx, docker.RunOptions{
		Image:         config.WorkerImage,
		Name:          workerContainerName(name),
		Environment:   a.WorkerEnvironment(),
		Volumes:       a.repoVolumes(name),
		WorkingDir:    config.WorkspaceDir,
		RestartPolicy: "unless-stopped",
	})
}

func (a *App) createShellContainer(ctx context.Context, name string) (docker.Container, error) {
	return a.Docker.RunContainer(ctx, docker.RunOptions{
		Image:         config.ShellImage,
		Name:          shellContainerName(name),
		Environment:   a.ShellEnvironment(),
		Volumes:       a.repoVolumes(name),
		WorkingDir:    config.WorkspaceDir,
		RestartPolicy: "unless-stopped",
	})
}

func (a *App) ensureRepoContainers(ctx context.Context, name string) (docker.Container, docker.Container, error) {
	repo, _ := a.Repo(name)
	workerID, shellID := "", ""
	if repo != nil {
		workerID = repo.ContainerID
		shellID = repo.ShellContainerID
	}
	worker, err := a.getRunningContainer(ctx, workerID, workerContainerName(name))
	if err != nil {
		return nil, nil, err
	}
	if worker == nil {
		worker, err = a.createWorkerContainer(ctx, name)
		if err != nil {
			return nil, nil, err
		}
	}
	shell, err := a.getRunningContainer(ctx, shellID, shellContainerName(name))
	if err != nil {
		return worker, nil, err
	}
	if shell == nil {
		shell, err = a.createShellContainer(ctx, name)
		if err != nil {
			return worker, nil, err
		}
	}
	return worker, shell, nil
}

func (a *App) recreateRepoContainers(ctx context.Context, name string) (docker.Container, error) {
	repo, _ := a.Repo(name)
	a.removeRepoContainers(ctx, name, repo)
	worker, shell, err := a.ensureRepoContainers(ctx, name)
	if err != nil {
		return nil, err
	}
	if repo == nil {
		repo = &state.Repo{}
	}
	repo.ContainerID = worker.ID()
	repo.ShellContainerID = shell.ID()
	a.SetRepo(name, repo)
	return worker, nil
}

type setupFailure struct {
	message string
}

func (a *App) setupRepo(ctx context.Context, status *telegram.Message, name, remoteURL string) (bool, bool) {
	baseContainer := a.BaseContainer()
	repoDir := a.repoBasePath(name)

	fail := func(failure setupFailure) (bool, bool) {
		a.editStatus(ctx, status, failure.message)
		return false, false
	}

	workspace, err := InspectWorkspace(ctx, baseContainer, repoDir)
	if err != nil {
		a.Logger.Printf("Error inspecting workspace: %v", err)
	}
	switch workspace {
	case "not-dir":
		return fail(setupFailure{
			message: fmt.Sprintf("`%s` already exists and is not a directory. Refusing to set up `%s`.", repoDir, name),
		})
	case "non-git":
		return fail(setupFailure{
			message: fmt.Sprintf("Directory `%s` is not empty and is not a git repository. Refusing to set up `%s`.", repoDir, name),
		})
	case "git-locked":
		return fail(setupFailure{
			message: fmt.Sprintf("`%s` has a stale `index.lock` (another git process may be running). "+
				"Refusing to set up `%s`.", repoDir, name),
		})
	}

	if workspace == "missing" {
		if err := EnsureContainerDirectory(ctx, baseContainer, repoDir); err != nil {
			return fail(setupFailure{
				message: fmt.Sprintf("Could not prepare `%s`: `%v`", repoDir, err),
			})
		}
		workspace = "empty"
	}

	workspacePath := repoDir
	if workspace == "git" {
		a.editStatus(ctx, status, fmt.Sprintf("Verifying remote of existing clone for `%s`...", name))
		remoteResult, _ := RunGit(ctx, baseContainer, workspacePath, []string{"config", "--get", "remote.origin.url"}, nil)
		existingURL := strings.TrimSpace(string(remoteResult.Output))
		if remoteResult.ExitCode != 0 || existingURL == "" {
			return fail(setupFailure{message: fmt.Sprintf("Existing directory `%s` is a git repository without an `origin` remote. "+
				"Refusing to set up `%s`.", name, name)})
		}
		if NormalizeRepoURL(existingURL) != NormalizeRepoURL(remoteURL) {
			return fail(setupFailure{message: fmt.Sprintf("Remote URL mismatch for `%s`:\n"+
				"*existing:* `%s`\n*requested:* `%s`\nRefusing to set up `%s`.", name, existingURL, remoteURL, name)})
		}
		a.editStatus(ctx, status, fmt.Sprintf("Existing clone found for `%s` with matching remote. Pulling latest changes...", name))
		updateResult, _ := RunGit(ctx, baseContainer, workspacePath, []string{"pull", "--ff-only"}, []string{"rebase.autoStash=true"})
		if updateResult.ExitCode != 0 {
			details := string(updateResult.Output)
			return fail(setupFailure{message: fmt.Sprintf("Could not update existing repository `%s`:\n%s\n"+
				"Resolve the conflict (or remove the directory) and try again.", name, opencode.FormatUntrustedOutput(details, 2000))})
		}
	} else {
		a.editStatus(ctx, status, fmt.Sprintf("Cloning `%s` into %s...", remoteURL, repoDir))
		cloneResult, _ := RunGit(ctx, baseContainer, workspacePath, []string{"clone", remoteURL, "."}, nil)
		if cloneResult.ExitCode != 0 {
			details := string(cloneResult.Output)
			return fail(setupFailure{message: "Failed to clone repository:\n" + opencode.FormatUntrustedOutput(details, 2000)})
		}
	}

	existing := &state.Repo{}
	if current, ok := a.Repo(name); ok {
		copy := *current
		copy.Mounts = append([]string(nil), current.Mounts...)
		copy.Extra = make(map[string]json.RawMessage, len(current.Extra))
		for key, value := range current.Extra {
			copy.Extra[key] = append(json.RawMessage(nil), value...)
		}
		existing = &copy
	}
	delete(existing.Extra, "commands")
	if a.Cfg.Catalog.Entry(existing.Model) == nil {
		existing.Model = a.Cfg.Catalog.DefaultID
		existing.Variant = a.Cfg.Catalog.DefaultVariant
		existing.VariantSet = true
	} else if !existing.VariantSet {
		existing.VariantSet = true
		if existing.Model == a.Cfg.Catalog.DefaultID {
			existing.Variant = a.Cfg.Catalog.DefaultVariant
		} else {
			existing.Variant = nil
		}
	}
	existing.URL = remoteURL
	existing.ContainerID = ""
	existing.ShellContainerID = ""
	a.SetRepo(name, existing)
	return true, workspace == "git"
}

// CmdCreate implements /create.
func (a *App) CmdCreate(ctx context.Context, update *telegram.Update, args []string) error {
	if !a.Authorized(update) {
		return nil
	}
	if len(args) < 2 {
		_, err := a.replyMarkdown(ctx, update, "Usage: `/create {name} {remote url}`")
		return err
	}
	name := strings.TrimSpace(args[0])
	remoteURL := strings.TrimSpace(args[1])
	if a.RepoExists(name) {
		_, err := a.replyMarkdown(ctx, update, fmt.Sprintf("Repository `%s` already exists.", name))
		return err
	}

	chatID, _ := messageTarget(update)
	status, err := a.replyMarkdown(ctx, update, fmt.Sprintf("Setting up repository `%s`...", name))
	if err != nil {
		return err
	}
	lockCtx, release, err := a.Locks.Acquire(ctx, name)
	if err != nil {
		return err
	}
	defer release()

	if a.RepoExists(name) {
		a.editStatus(lockCtx, status, fmt.Sprintf("Repository `%s` already exists.", name))
		return nil
	}
	ok, hadClone := a.setupRepo(lockCtx, status, name, remoteURL)
	if !ok {
		return nil
	}
	a.RemovePendingPlanButton(lockCtx, chatID)
	a.SetActiveContext(chatID, name)
	a.Save()
	state := "initialized"
	if hadClone {
		state = "updated"
	}
	a.editStatus(lockCtx, status, fmt.Sprintf("Repository `%s` %s and set as active context! Containers will start on first use.\nModel configured: `%s`", name, state, a.ModelForRepo(name)))
	return nil
}

// CmdSet implements /set.
func (a *App) CmdSet(ctx context.Context, update *telegram.Update, args []string) error {
	if !a.Authorized(update) {
		return nil
	}
	if len(args) == 0 {
		_, err := a.replyMarkdown(ctx, update, "Usage: `/set {name}`")
		return err
	}
	name := strings.TrimSpace(args[0])
	if !a.RepoExists(name) {
		_, err := a.replyMarkdown(ctx, update, fmt.Sprintf("Repo `%s` not found. Use `/list` to see available repos.", name))
		return err
	}
	chatID, _ := messageTarget(update)
	if a.ActiveName(chatID) != name {
		a.RemovePendingPlanButton(ctx, chatID)
	}
	a.SetActiveContext(chatID, name)
	a.Save()
	_, err := a.replyMarkdown(ctx, update, fmt.Sprintf("Active context set to `%s`.", name))
	return err
}

// CmdInfo implements /info.
func (a *App) CmdInfo(ctx context.Context, update *telegram.Update, args []string) error {
	if !a.Authorized(update) {
		return nil
	}
	chatID, _ := messageTarget(update)
	name := a.ActiveRepo(chatID)
	if name == "" {
		_, err := a.replyMarkdown(ctx, update, "No active repository set. Use `/set {name}` or `/create`.")
		return err
	}
	repo, _ := a.Repo(name)
	workerStatus := a.containerStatus(ctx, repo.ContainerID, workerContainerName(name))
	shellStatus := a.containerStatus(ctx, repo.ShellContainerID, shellContainerName(name))
	modelID := a.ModelForRepo(name)
	label := a.Cfg.Catalog.Label(modelID)
	variantLine := ""
	if variant := a.VariantForRepo(name); variant != nil {
		variantLine = fmt.Sprintf("\n**Variant:** `%s`", *variant)
	}
	message := fmt.Sprintf("**Active Context:** `%s`\n**Remote URL:** %s\n**Worker Status:** `%s`\n**Shell Status:** `%s`\n**Model:** %s (`%s`)%s",
		name, repo.URL, workerStatus, shellStatus, label, modelID, variantLine)
	_, err := a.replyMarkdown(ctx, update, message)
	return err
}

func (a *App) containerStatus(ctx context.Context, id, name string) string {
	reference := id
	if reference == "" {
		reference = name
	}
	container, err := a.Docker.GetContainer(ctx, reference)
	if err != nil {
		if errors.Is(err, docker.ErrNotFound) {
			return "Container Not Found"
		}
		return "Unknown"
	}
	status, err := container.Status(ctx)
	if err != nil {
		return "Unknown"
	}
	return status
}

// CmdList implements /list.
func (a *App) CmdList(ctx context.Context, update *telegram.Update, args []string) error {
	if !a.Authorized(update) {
		return nil
	}
	names := a.RepoKeys()
	if len(names) == 0 {
		_, err := a.replyMarkdown(ctx, update, "No repositories configured yet. Use `/create`.")
		return err
	}
	chatID, _ := messageTarget(update)
	current := a.ActiveName(chatID)
	lines := []string{"**Configured Repositories:**\n"}
	for _, name := range names {
		repo, _ := a.Repo(name)
		prefix := "• "
		if name == current {
			prefix = "👉 "
		}
		lines = append(lines, fmt.Sprintf("%s`%s`: %s", prefix, name, repo.URL))
	}
	_, err := a.replyMarkdown(ctx, update, strings.Join(lines, "\n"))
	return err
}

func (a *App) safeWorkspacePath(name string) (string, bool) {
	workspace := a.repoBasePath(name)
	normalized := path.Clean(workspace)
	base := path.Base(normalized)
	if path.Dir(normalized) != path.Clean(a.Cfg.ReposDir) || base == "" || base == "." || base == ".." {
		return "", false
	}
	return normalized, true
}

func (a *App) changeMount(ctx context.Context, update *telegram.Update, mount string, adding bool) error {
	chatID, _ := messageTarget(update)
	active := a.ActiveName(chatID)
	if active == "" {
		_, err := a.replyMarkdown(ctx, update, config.NoActiveRepoMessage)
		return err
	}
	if _, ok := a.safeWorkspacePath(mount); !ok {
		_, err := a.replyMarkdown(ctx, update, fmt.Sprintf("Invalid repository name `%s`.", mount))
		return err
	}
	if adding {
		if mount == active {
			_, err := a.replyMarkdown(ctx, update, "Cannot mount the active repository onto itself.")
			return err
		}
		if !a.RepoExists(mount) {
			_, err := a.replyMarkdown(ctx, update, fmt.Sprintf("Repo `%s` not found. Use `/list` to see available repos.", mount))
			return err
		}
	}

	lockCtx, release, err := a.Locks.Acquire(ctx, active)
	if err != nil {
		return err
	}
	defer release()

	repo, ok := a.Repo(active)
	if !ok {
		_, err := a.replyMarkdown(lockCtx, update, config.NoActiveRepoMessage)
		return err
	}
	oldMounts := append([]string(nil), repo.Mounts...)
	hasMount := false
	for _, existing := range oldMounts {
		if existing == mount {
			hasMount = true
			break
		}
	}
	if adding && hasMount {
		_, err := a.replyMarkdown(lockCtx, update, fmt.Sprintf("Repo `%s` is already mounted at `%s/%s` in `%s`.", mount, config.MountDir, mount, active))
		return err
	}
	if !adding && !hasMount {
		_, err := a.replyMarkdown(lockCtx, update, fmt.Sprintf("Repo `%s` is not mounted in `%s`.", mount, active))
		return err
	}

	newMounts := append([]string(nil), oldMounts...)
	statusText := fmt.Sprintf("Mounting `%s` into `%s`...", mount, active)
	if adding {
		newMounts = append(newMounts, mount)
	} else {
		statusText = fmt.Sprintf("Unmounting `%s` from `%s`...", mount, active)
		filtered := newMounts[:0]
		for _, existing := range newMounts {
			if existing != mount {
				filtered = append(filtered, existing)
			}
		}
		newMounts = filtered
	}
	status, err := a.replyMarkdown(lockCtx, update, statusText)
	if err != nil {
		return err
	}
	repo.Mounts = newMounts
	a.SetRepo(active, repo)
	if _, err := a.recreateRepoContainers(lockCtx, active); err != nil {
		action := "unmounting"
		if adding {
			action = "mounting"
		}
		a.Logger.Printf("Error %s %s: %v", action, mount, err)
		repo.Mounts = oldMounts
		a.SetRepo(active, repo)
		a.editStatus(lockCtx, status, fmt.Sprintf("Failed to %s `%s`: `%v`", map[bool]string{true: "mount", false: "unmount"}[adding], mount, err))
		return nil
	}
	a.resetRepoSessions(lockCtx, active)
	a.Save()
	if adding {
		a.editStatus(lockCtx, status, fmt.Sprintf("Mounted `%s` at `%s/%s` in `%s`.", mount, config.MountDir, mount, active))
	} else {
		a.editStatus(lockCtx, status, fmt.Sprintf("Unmounted `%s` from `%s`.", mount, active))
	}
	return nil
}

// CmdMount implements /mount.
func (a *App) CmdMount(ctx context.Context, update *telegram.Update, args []string) error {
	if !a.Authorized(update) {
		return nil
	}
	if len(args) == 0 {
		_, err := a.replyMarkdown(ctx, update, "Usage: `/mount {name}`")
		return err
	}
	return a.changeMount(ctx, update, strings.TrimSpace(args[0]), true)
}

// CmdUnmount implements /unmount.
func (a *App) CmdUnmount(ctx context.Context, update *telegram.Update, args []string) error {
	if !a.Authorized(update) {
		return nil
	}
	if len(args) == 0 {
		_, err := a.replyMarkdown(ctx, update, "Usage: `/unmount {name}`")
		return err
	}
	return a.changeMount(ctx, update, strings.TrimSpace(args[0]), false)
}

// CmdStop implements /stop.
func (a *App) CmdStop(ctx context.Context, update *telegram.Update, args []string) error {
	if !a.Authorized(update) {
		return nil
	}
	if len(args) == 0 {
		_, err := a.replyMarkdown(ctx, update, "Usage: `/stop {name}`")
		return err
	}
	name := strings.TrimSpace(args[0])
	if !a.RepoExists(name) {
		_, err := a.replyMarkdown(ctx, update, fmt.Sprintf("Repo `%s` does not exist.", name))
		return err
	}
	status, err := a.replyMarkdown(ctx, update, fmt.Sprintf("Stopping `%s`...", name))
	if err != nil {
		return err
	}
	lockCtx, release, err := a.Locks.Acquire(ctx, name)
	if err != nil {
		return err
	}
	defer release()
	repo, ok := a.Repo(name)
	if !ok {
		a.editStatus(lockCtx, status, fmt.Sprintf("Repo `%s` does not exist.", name))
		return nil
	}
	a.removeRepoContainers(lockCtx, name, repo)
	repo.ContainerID = ""
	repo.ShellContainerID = ""
	a.SetRepo(name, repo)
	a.resetRepoSessions(lockCtx, name)
	a.Save()
	a.editStatus(lockCtx, status, fmt.Sprintf("Stopped `%s`: containers removed, repository state kept.", name))
	return nil
}

func (a *App) unpushedChanges(ctx context.Context, shell docker.Container, workspacePath string) string {
	run := func(args []string) docker.ExecResult {
		result, _ := RunGit(ctx, shell, workspacePath, args, nil)
		return result
	}
	status := run([]string{"status", "--porcelain"})
	if status.ExitCode != 0 {
		return "could not read the working tree status"
	}
	if strings.TrimSpace(string(status.Output)) != "" {
		return "the working tree has uncommitted or untracked changes"
	}
	remotes := run([]string{"remote"})
	if remotes.ExitCode != 0 || strings.TrimSpace(string(remotes.Output)) == "" {
		return "the repository has no remote to verify pushed changes against"
	}
	unpushed := run([]string{"rev-list", "--count", "HEAD", "--not", "--remotes"})
	if unpushed.ExitCode != 0 {
		return "could not compare the checkout with its remote"
	}
	var count int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(unpushed.Output)), "%d", &count); err != nil {
		return "could not compare the checkout with its remote"
	}
	if count != 0 {
		return fmt.Sprintf("%d commit(s) have not been pushed", count)
	}
	return ""
}

// CmdPurge implements /purge.
func (a *App) CmdPurge(ctx context.Context, update *telegram.Update, args []string) error {
	if !a.Authorized(update) {
		return nil
	}
	if len(args) == 0 {
		_, err := a.replyMarkdown(ctx, update, "Usage: `/purge {name}`")
		return err
	}
	name := strings.TrimSpace(args[0])
	workspacePath, ok := a.safeWorkspacePath(name)
	if !ok {
		_, err := a.replyMarkdown(ctx, update, fmt.Sprintf("Refusing to purge `%s`: the name does not map to a repository directory.", name))
		return err
	}
	status, err := a.replyMarkdown(ctx, update, fmt.Sprintf("Purging `%s`...", name))
	if err != nil {
		return err
	}
	lockCtx, release, err := a.Locks.Acquire(ctx, name)
	if err != nil {
		return err
	}
	defer release()
	repo, hadRepo := a.Repo(name)
	baseContainer := a.BaseContainer()
	workspace, _ := InspectWorkspace(lockCtx, baseContainer, workspacePath)
	if workspace == "missing" && !hadRepo {
		a.editStatus(lockCtx, status, fmt.Sprintf("No checkout named `%s` exists to purge.", name))
		return nil
	}
	if workspace == "non-git" || workspace == "git-locked" {
		reason := "it is not a git repository"
		if workspace == "git-locked" {
			reason = "it has a stale `index.lock`"
		}
		a.editStatus(lockCtx, status, fmt.Sprintf("Refusing to purge `%s`: %s, so unpushed changes cannot be verified.", name, reason))
		return nil
	}
	if workspace == "not-dir" {
		a.editStatus(lockCtx, status, fmt.Sprintf("Refusing to purge `%s`: `%s` exists and is not a directory.", name, workspacePath))
		return nil
	}
	if workspace == "git" {
		if reason := a.unpushedChanges(lockCtx, baseContainer, workspacePath); reason != "" {
			a.editStatus(lockCtx, status, fmt.Sprintf("Refusing to purge `%s`: %s.", name, reason))
			return nil
		}
	}
	if hadRepo {
		a.removeRepoContainers(lockCtx, name, repo)
	}
	if workspace != "missing" {
		result, err := baseContainer.Exec(lockCtx, docker.ExecOptions{
			Cmd:     []string{"/bin/bash", "-c", "rm -rf -- " + shellQuote(workspacePath)},
			Workdir: "/",
		})
		if err != nil || result.ExitCode != 0 {
			output := string(result.Output)
			a.editStatus(lockCtx, status, fmt.Sprintf("Could not remove `%s`:\n%s", workspacePath, opencode.FormatUntrustedOutput(output, 2000)))
			return nil
		}
	}
	a.forgetRepo(lockCtx, name)
	switch {
	case !hadRepo:
		a.editStatus(lockCtx, status, fmt.Sprintf("Checkout `%s` was purged from disk.", name))
	case workspace == "missing":
		a.editStatus(lockCtx, status, fmt.Sprintf("Repository `%s` had no checkout left on disk; its containers and state were removed.", name))
	default:
		a.editStatus(lockCtx, status, fmt.Sprintf("Repository `%s` was purged from disk and its containers removed.", name))
	}
	return nil
}

func (a *App) reloadRepo(ctx context.Context, status *telegram.Message, name string) (bool, bool) {
	repo, ok := a.Repo(name)
	if !ok {
		return false, false
	}
	a.removeRepoContainers(ctx, name, repo)
	a.resetRepoSessions(ctx, name)
	ok, _ = a.setupRepo(ctx, status, name, repo.URL)
	return ok, true
}

// CmdReload implements /reload.
func (a *App) CmdReload(ctx context.Context, update *telegram.Update, args []string) error {
	if !a.Authorized(update) {
		return nil
	}
	if len(args) == 0 {
		_, err := a.replyMarkdown(ctx, update, "Usage: `/reload {name}`")
		return err
	}
	name := strings.TrimSpace(args[0])
	if !a.RepoExists(name) {
		_, err := a.replyMarkdown(ctx, update, fmt.Sprintf("Repo `%s` does not exist.", name))
		return err
	}
	status, err := a.replyMarkdown(ctx, update, fmt.Sprintf("Reloading `%s`...", name))
	if err != nil {
		return err
	}
	lockCtx, release, err := a.Locks.Acquire(ctx, name)
	if err != nil {
		return err
	}
	defer release()
	ok, existed := a.reloadRepo(lockCtx, status, name)
	if !existed {
		a.editStatus(lockCtx, status, fmt.Sprintf("Repo `%s` does not exist.", name))
		return nil
	}
	if !ok {
		return nil
	}
	chatID, _ := messageTarget(update)
	a.RemovePendingPlanButton(lockCtx, chatID)
	a.SetActiveContext(chatID, name)
	a.Save()
	a.editStatus(lockCtx, status, fmt.Sprintf("Repository `%s` reloaded and set as active context!\nModel configured: `%s`", name, a.ModelForRepo(name)))
	return nil
}

// CmdReloadAll implements /reloadall.
func (a *App) CmdReloadAll(ctx context.Context, update *telegram.Update, args []string) error {
	if !a.Authorized(update) {
		return nil
	}
	names := a.RepoKeys()
	if len(names) == 0 {
		_, err := a.replyMarkdown(ctx, update, "No repositories configured yet. Use `/create`.")
		return err
	}
	total := len(names)
	status, err := a.replyMarkdown(ctx, update, fmt.Sprintf("Reloading all %d repositories...", total))
	if err != nil {
		return err
	}
	var reloaded, failed, skipped []string
	for index, name := range names {
		if !a.RepoExists(name) {
			skipped = append(skipped, name)
			continue
		}
		a.editStatus(ctx, status, fmt.Sprintf("Reloading `%s` (%d/%d)...", name, index+1, total))
		lockCtx, release, err := a.Locks.Acquire(ctx, name)
		if err != nil {
			failed = append(failed, name)
			continue
		}
		ok, existed := a.reloadRepo(lockCtx, status, name)
		release()
		switch {
		case !existed:
			skipped = append(skipped, name)
		case ok:
			reloaded = append(reloaded, name)
			a.Save()
		default:
			failed = append(failed, name)
		}
	}
	summary := fmt.Sprintf("Reloaded %d/%d repositories.", len(reloaded), total)
	if len(failed) > 0 {
		summary += "\nFailed: " + quoteList(failed)
	}
	if len(skipped) > 0 {
		summary += "\nSkipped (no longer configured): " + quoteList(skipped)
	}
	a.editStatus(ctx, status, summary)
	return nil
}

func quoteList(names []string) string {
	quoted := make([]string, len(names))
	for index, name := range names {
		quoted[index] = "`" + name + "`"
	}
	return strings.Join(quoted, ", ")
}

var ulCaptionPattern = regexp.MustCompile(`(?is)^/ul(?:@\w+)?(?:\s+(.*))?$`)

func defaultUploadName(message *telegram.Message, attachment *telegram.Attachment) string {
	extension := ""
	switch {
	case len(message.Photo) > 0:
		extension = ".jpg"
	case message.Video != nil:
		extension = ".mp4"
	case message.Audio != nil:
		extension = ".mp3"
	case message.Voice != nil:
		extension = ".ogg"
	case message.VideoNote != nil:
		extension = ".mp4"
	case message.Animation != nil:
		extension = ".mp4"
	case message.Sticker != nil:
		extension = ".webp"
	}
	if extension == "" && attachment != nil && attachment.MimeType != "" {
		extensions, _ := mime.ExtensionsByType(attachment.MimeType)
		if len(extensions) > 0 {
			extension = extensions[0]
		}
	}
	return TelegramUploadName(extension)
}

func attachmentAndName(message *telegram.Message) (*telegram.Attachment, string) {
	var attachment *telegram.Attachment
	switch {
	case message.Document != nil:
		attachment = message.Document
	case message.Video != nil:
		attachment = message.Video
	case message.Audio != nil:
		attachment = message.Audio
	case message.Voice != nil:
		attachment = message.Voice
	case message.VideoNote != nil:
		attachment = message.VideoNote
	case message.Animation != nil:
		attachment = message.Animation
	case message.Sticker != nil:
		attachment = message.Sticker
	case len(message.Photo) > 0:
		last := message.Photo[len(message.Photo)-1]
		attachment = &telegram.Attachment{FileID: last.FileID}
	}
	if attachment == nil {
		return nil, ""
	}
	name := attachment.FileName
	if name == "" {
		name = defaultUploadName(message, attachment)
	}
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		name = defaultUploadName(message, attachment)
	}
	return attachment, name
}

func uploadPathFromMessage(message *telegram.Message, args []string) string {
	if message.Caption != "" {
		match := ulCaptionPattern.FindStringSubmatch(strings.TrimSpace(message.Caption))
		if match != nil {
			return strings.TrimSpace(match[1])
		}
		return ""
	}
	return strings.TrimSpace(strings.Join(args, " "))
}

func (a *App) resolveUploadTarget(rawPath, existingKind, originalName string) (string, string, string) {
	trimmed := strings.TrimSpace(rawPath)
	wantsDirectory := strings.HasSuffix(trimmed, "/") || strings.HasSuffix(trimmed, "\\")
	resolved := a.ResolveShellPath(trimmed)
	if wantsDirectory && existingKind == "file" {
		return "", "", fmt.Sprintf("`%s` exists and is not a directory.", resolved)
	}
	if wantsDirectory || existingKind == "dir" {
		return resolved, originalName, ""
	}
	parent := path.Dir(resolved)
	if parent == "" {
		parent = "/"
	}
	filename := path.Base(resolved)
	if filename == "" || filename == "." || filename == ".." {
		return "", "", fmt.Sprintf("Invalid upload path `%s`.", resolved)
	}
	return parent, filename, ""
}

// CmdUL implements /ul and attachment captions.
func (a *App) CmdUL(ctx context.Context, update *telegram.Update, args []string) error {
	if !a.Authorized(update) || update.Message == nil {
		return nil
	}
	message := update.Message
	rawPath := uploadPathFromMessage(message, args)
	attachment, originalName := attachmentAndName(message)
	if attachment == nil {
		_, err := a.replyMarkdown(ctx, update, "Usage: attach a file with the caption `/ul {path}`.")
		return err
	}
	if rawPath == "" {
		_, err := a.replyMarkdown(ctx, update, "Usage: `/ul {path}` (absolute or relative to /workspace)")
		return err
	}
	chatID, _ := messageTarget(update)
	name := a.ActiveRepo(chatID)
	if name == "" {
		_, err := a.replyMarkdown(ctx, update, config.NoActiveRepoMessage)
		return err
	}
	lockCtx, release, err := a.Locks.Acquire(ctx, name)
	if err != nil {
		return err
	}
	defer release()
	status, err := a.replyMarkdown(lockCtx, update, fmt.Sprintf("Preparing upload to `%s`...", name))
	if err != nil {
		return err
	}
	shell, err := a.GetShellContainer(lockCtx, name)
	if err != nil || shell == nil {
		a.editStatus(lockCtx, status, fmt.Sprintf("Shell container for `%s` is not available.", name))
		return nil
	}
	resolved := a.ResolveShellPath(rawPath)
	kind, err := DescribeContainerPath(lockCtx, shell, resolved)
	if err != nil {
		a.reportStatus(lockCtx, update, status, fmt.Sprintf("Failed to upload `%s`: `%v`", rawPath, err))
		return nil
	}
	targetDir, filename, problem := a.resolveUploadTarget(rawPath, kind, originalName)
	if problem != "" {
		a.editStatus(lockCtx, status, problem)
		return nil
	}
	if err := EnsureContainerDirectory(lockCtx, shell, targetDir); err != nil {
		a.reportStatus(lockCtx, update, status, fmt.Sprintf("Failed to upload `%s`: `%v`", rawPath, err))
		return nil
	}
	data, _, err := a.Telegram.GetFileBytes(lockCtx, attachment.FileID)
	if err != nil {
		a.reportStatus(lockCtx, update, status, fmt.Sprintf("Failed to upload `%s`: `%v`", rawPath, err))
		return nil
	}
	if err := CopyToContainer(lockCtx, shell, data, filename, targetDir); err != nil {
		a.reportStatus(lockCtx, update, status, fmt.Sprintf("Failed to upload `%s`: `%v`", rawPath, err))
		return nil
	}
	destination := path.Join(targetDir, filename)
	a.editStatus(lockCtx, status, fmt.Sprintf("Uploaded `%s` to `%s` (%s bytes).", filename, destination, commaInt(len(data))))
	return nil
}

func (a *App) reportStatus(ctx context.Context, update *telegram.Update, status *telegram.Message, text string) {
	if status != nil {
		if err := a.Telegram.EditMessageText(ctx, status.ChatID, status.ID, text, telegram.SendOptions{Markdown: true}); err == nil {
			return
		}
	}
	if _, err := a.replyMarkdown(ctx, update, text); err != nil {
		a.Logger.Printf("Could not report status to the chat: %v", err)
	}
}

// CmdDL implements /dl.
func (a *App) CmdDL(ctx context.Context, update *telegram.Update, args []string) error {
	if !a.Authorized(update) {
		return nil
	}
	if len(args) == 0 {
		_, err := a.replyMarkdown(ctx, update, "Usage: `/dl {path}` (absolute or relative to /workspace)")
		return err
	}
	rawPath := strings.TrimSpace(strings.Join(args, " "))
	chatID, _ := messageTarget(update)
	name := a.ActiveRepo(chatID)
	if name == "" {
		_, err := a.replyMarkdown(ctx, update, config.NoActiveRepoMessage)
		return err
	}
	lockCtx, release, err := a.Locks.Acquire(ctx, name)
	if err != nil {
		return err
	}
	defer release()
	shell, err := a.GetShellContainer(lockCtx, name)
	if err != nil || shell == nil {
		_, replyErr := a.replyMarkdown(lockCtx, update, fmt.Sprintf("Shell container for `%s` is not available.", name))
		return replyErr
	}
	filePath := a.ResolveShellPath(rawPath)
	_ = a.Telegram.SendChatAction(lockCtx, chatID, "upload_document")
	kind, err := DescribeContainerPath(lockCtx, shell, filePath)
	if err != nil {
		_, err := a.replyMarkdown(lockCtx, update, fmt.Sprintf("Could not inspect `%s`: `%v`", filePath, err))
		return err
	}
	if kind == "" {
		_, err := a.replyMarkdown(lockCtx, update, fmt.Sprintf("Path `%s` does not exist in repository `%s`.", filePath, name))
		return err
	}
	status, err := a.replyMarkdown(lockCtx, update, fmt.Sprintf("Packaging `%s`...", filePath))
	if err != nil {
		return err
	}
	var payload []byte
	var downloadName, caption string
	if kind == "file" {
		payload, err = ReadFileFromContainer(lockCtx, shell, filePath)
		if err != nil {
			a.reportStatus(lockCtx, update, status, fmt.Sprintf("Failed to send `%s`: `%v`", filePath, err))
			return nil
		}
		if len(payload) > config.MaxUploadBytes {
			a.editStatus(lockCtx, status, fmt.Sprintf("`%s` is %s bytes, which exceeds the %d MB Telegram limit.", filePath, commaInt(len(payload)), config.MaxUploadBytes/(1024*1024)))
			return nil
		}
		downloadName = path.Base(filePath)
		if downloadName == "" || downloadName == "." || downloadName == "/" {
			downloadName = name + ".txt"
		}
		caption = fmt.Sprintf("`%s` (%s bytes)", filePath, commaInt(len(payload)))
	} else {
		payload, err = TarDirectoryInContainer(lockCtx, shell, filePath)
		if err != nil {
			a.reportStatus(lockCtx, update, status, fmt.Sprintf("Failed to send `%s`: `%v`", filePath, err))
			return nil
		}
		base := path.Base(strings.TrimRight(filePath, "/"))
		if base == "" || base == "." || base == "/" {
			base = name
		}
		downloadName = base + ".tar.gz"
		caption = fmt.Sprintf("Directory `%s` compressed (%s bytes)", filePath, commaInt(len(payload)))
	}
	if _, err := a.Telegram.SendDocument(lockCtx, chatID, downloadName, payload, caption, telegram.SendOptions{Markdown: true, ReplyTo: update.Message.ID}); err != nil {
		a.reportStatus(lockCtx, update, status, fmt.Sprintf("Failed to send `%s`: `%v`", filePath, err))
		return nil
	}
	a.DeleteMessageBestEffort(lockCtx, status.ChatID, status.ID)
	return nil
}
