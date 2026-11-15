package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"opencodebot/internal/config"
	"opencodebot/internal/docker"
	"opencodebot/internal/telegram"
)

func (a *App) runContainer(ctx context.Context, entry *runEntry, name string) (docker.Container, error) {
	if entry.ContainerID == a.BaseContainer().ID() {
		return a.BaseContainer(), nil
	}
	if entry.ContainerID == "" {
		return a.GetRepoContainer(ctx, name)
	}
	container, err := a.Docker.GetContainer(ctx, entry.ContainerID)
	if err != nil {
		if errors.Is(err, docker.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return container, nil
}

// CmdCancel implements /cancel [repo].
func (a *App) CmdCancel(ctx context.Context, update *telegram.Update, args []string) error {
	if !a.Authorized(update) {
		return nil
	}
	chatID, _ := messageTarget(update)
	requested := strings.TrimSpace(strings.Join(args, " "))
	name := requested
	if name == "" {
		name = a.ActiveRepo(chatID)
	}
	if name == "" || !a.RepoExists(name) {
		_, err := a.replyMarkdown(ctx, update, config.NoActiveRepoMessage)
		return err
	}
	entry, hadRun := a.requestCancel(name)
	if !hadRun {
		_, err := a.replyMarkdown(ctx, update, fmt.Sprintf("No command is running in `%s`.", name))
		return err
	}
	container, err := a.runContainer(ctx, entry, name)
	if err != nil || container == nil {
		_, err := a.replyMarkdown(ctx, update, fmt.Sprintf("Cancelled the pending run in `%s`; its container is no longer available.", name))
		return err
	}
	killed, err := KillRunProcesses(ctx, container, entry.RunID)
	if err != nil {
		a.Logger.Printf("Failed to signal the run in %s: %v", name, err)
		killed = false
	}
	if killed {
		_, err := a.replyMarkdown(ctx, update, fmt.Sprintf("Cancelled the running command in `%s`.", name))
		return err
	}
	for attempt := 0; attempt < 20; attempt++ {
		if !a.runStillActive(name, entry.RunID) {
			_, err := a.replyMarkdown(ctx, update, fmt.Sprintf("Cancelled the running command in `%s`.", name))
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	if container.ID() == a.BaseContainer().ID() {
		_, err := a.replyMarkdown(ctx, update, fmt.Sprintf("Could not terminate the command in `%s`.", name))
		return err
	}
	if err := RestartContainer(ctx, container); err != nil {
		a.Logger.Printf("Failed to restart worker %s: %v", name, err)
		_, replyErr := a.replyMarkdown(ctx, update, fmt.Sprintf("Could not terminate the command in `%s`. Try `/reload %s`.", name, name))
		return replyErr
	}
	_, err = a.replyMarkdown(ctx, update, fmt.Sprintf("Forced the container for `%s` to restart to cancel the command.", name))
	return err
}
