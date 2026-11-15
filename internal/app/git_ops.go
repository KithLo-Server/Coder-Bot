package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"opencodebot/internal/config"
	"opencodebot/internal/docker"
	"opencodebot/internal/opencode"
	"opencodebot/internal/telegram"
)

func runGitTuple(ctx context.Context, shell docker.Container, workspacePath string, args []string) (int, string) {
	result, err := RunGit(ctx, shell, workspacePath, args, nil)
	if err != nil {
		return -1, ""
	}
	return result.ExitCode, strings.TrimSpace(string(result.Output))
}

func rebaseInProgress(ctx context.Context, shell docker.Container, workspacePath string) bool {
	safeDirectory := GitConfigArgs(workspacePath, nil)
	script := fmt.Sprintf(`merge=$(git %s rev-parse --git-path rebase-merge 2>/dev/null) && [ -e "$merge" ] && exit 0; `+
		`apply=$(git %s rev-parse --git-path rebase-apply 2>/dev/null) && [ -e "$apply" ] && exit 0; exit 1`,
		safeDirectory, safeDirectory)
	result, err := shell.Exec(ctx, docker.ExecOptions{Cmd: []string{"/bin/bash", "-c", script}, Workdir: workspacePath})
	return err == nil && result.ExitCode == 0
}

func pullRebase(ctx context.Context, shell docker.Container, workspacePath string) (bool, bool, string) {
	exitCode, output := runGitTuple(ctx, shell, workspacePath, []string{"pull", "--rebase"})
	if exitCode == 0 {
		return true, false, output
	}
	conflicted := rebaseInProgress(ctx, shell, workspacePath)
	if conflicted {
		runGitTuple(ctx, shell, workspacePath, []string{"rebase", "--abort"})
	}
	return false, conflicted, output
}

func commitsBehind(ctx context.Context, shell docker.Container, workspacePath string) (int, bool) {
	exitCode, output := runGitTuple(ctx, shell, workspacePath, []string{"rev-list", "--count", "HEAD..@{u}"})
	if exitCode != 0 {
		return 0, false
	}
	count, err := strconv.Atoi(strings.TrimSpace(output))
	if err != nil {
		return 0, false
	}
	return count, true
}

func pushWithRebase(ctx context.Context, shell docker.Container, workspacePath string) (string, string) {
	exitCode, output := runGitTuple(ctx, shell, workspacePath, []string{"rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"})
	if exitCode != 0 || output == "" {
		return "no-upstream", output
	}
	exitCode, fetchOutput := runGitTuple(ctx, shell, workspacePath, []string{"fetch"})
	if exitCode != 0 {
		return "fetch-failed", fetchOutput
	}
	behind, ok := commitsBehind(ctx, shell, workspacePath)
	if !ok {
		return "check-failed", fetchOutput
	}
	if behind > 0 {
		ok, conflicted, pullOutput := pullRebase(ctx, shell, workspacePath)
		if !ok {
			if conflicted {
				return "conflict", pullOutput
			}
			return "pull-failed", pullOutput
		}
	}
	exitCode, pushOutput := runGitTuple(ctx, shell, workspacePath, []string{"push"})
	if exitCode == 0 {
		return "ok", pushOutput
	}

	exitCode, _ = runGitTuple(ctx, shell, workspacePath, []string{"fetch"})
	if exitCode != 0 {
		return "push-failed", pushOutput
	}
	behind, ok = commitsBehind(ctx, shell, workspacePath)
	if ok && behind > 0 {
		ok, conflicted, pullOutput := pullRebase(ctx, shell, workspacePath)
		if !ok {
			if conflicted {
				return "conflict", pullOutput
			}
			return "pull-failed", pullOutput
		}
		exitCode, pushOutput = runGitTuple(ctx, shell, workspacePath, []string{"push"})
		if exitCode == 0 {
			return "ok", pushOutput
		}
	}
	return "push-failed", pushOutput
}

func withOutput(message, output string) string {
	if output == "" {
		return message
	}
	return message + "\n" + opencode.FormatUntrustedOutput(output, 2000)
}

func (a *App) activeRepoForSync(ctx context.Context, update *telegram.Update) (int64, string, *telegram.Message, bool) {
	chatID, _ := messageTarget(update)
	name := a.ActiveRepo(chatID)
	if name == "" {
		_, _ = a.replyMarkdown(ctx, update, config.NoActiveRepoMessage)
		return 0, "", nil, false
	}
	status, err := a.replyMarkdown(ctx, update, fmt.Sprintf("Working on `%s`...", name))
	if err != nil {
		return 0, "", nil, false
	}
	return chatID, name, status, true
}

// CmdPull implements /pull.
func (a *App) CmdPull(ctx context.Context, update *telegram.Update, args []string) error {
	if !a.Authorized(update) {
		return nil
	}
	chatID, name, status, ok := a.activeRepoForSync(ctx, update)
	if !ok {
		return nil
	}
	lockCtx, release, err := a.Locks.Acquire(ctx, name)
	if err != nil {
		return err
	}
	defer release()
	shell, err := a.GetShellContainer(lockCtx)
	if err != nil || shell == nil {
		a.editStatus(lockCtx, status, fmt.Sprintf("Shell container for `%s` is not available.", name))
		return nil
	}
	_ = a.Telegram.SendChatAction(lockCtx, chatID, "typing")
	pulled, conflicted, output := pullRebase(lockCtx, shell, repoWorkspace(name))
	var message string
	switch {
	case pulled:
		message = fmt.Sprintf("Pulled `%s` with rebase.", name)
	case conflicted:
		message = fmt.Sprintf("Pull of `%s` conflicted. The rebase was aborted and nothing was applied.", name)
	default:
		message = fmt.Sprintf("Could not pull `%s`.", name)
	}
	a.editStatus(lockCtx, status, withOutput(message, output))
	return nil
}

// CmdPush implements /push.
func (a *App) CmdPush(ctx context.Context, update *telegram.Update, args []string) error {
	if !a.Authorized(update) {
		return nil
	}
	chatID, name, status, ok := a.activeRepoForSync(ctx, update)
	if !ok {
		return nil
	}
	lockCtx, release, err := a.Locks.Acquire(ctx, name)
	if err != nil {
		return err
	}
	defer release()
	shell, err := a.GetShellContainer(lockCtx)
	if err != nil || shell == nil {
		a.editStatus(lockCtx, status, fmt.Sprintf("Shell container for `%s` is not available.", name))
		return nil
	}
	_ = a.Telegram.SendChatAction(lockCtx, chatID, "typing")
	statusName, output := pushWithRebase(lockCtx, shell, repoWorkspace(name))
	var message string
	switch statusName {
	case "ok":
		message = fmt.Sprintf("Pushed `%s`.", name)
	case "no-upstream":
		message = fmt.Sprintf("Cannot push `%s`: no upstream branch is configured. "+
			"Set one with `/sh git branch --set-upstream-to=origin/<branch>`.", name)
	case "conflict":
		message = fmt.Sprintf("Push of `%s` could not proceed: the pull-rebase conflicted and was "+
			"aborted, so nothing was pushed.", name)
	case "fetch-failed":
		message = fmt.Sprintf("Could not fetch the upstream of `%s`, so nothing was pushed.", name)
	case "check-failed":
		message = fmt.Sprintf("Could not compare `%s` with its upstream, so nothing was pushed.", name)
	case "pull-failed":
		message = fmt.Sprintf("Could not rebase `%s` onto upstream, so nothing was pushed.", name)
	default:
		message = fmt.Sprintf("Could not push `%s`.", name)
	}
	a.editStatus(lockCtx, status, withOutput(message, output))
	return nil
}
