package app

import (
	"context"
	"fmt"
	"strings"

	"opencodebot/internal/config"
	"opencodebot/internal/telegram"
)

// CmdSh implements /sh.
func (a *App) CmdSh(ctx context.Context, update *telegram.Update, args []string) error {
	if !a.Authorized(update) {
		return nil
	}
	command := strings.Join(args, " ")
	if command == "" {
		_, err := a.replyMarkdown(ctx, update, "Usage: `/sh {command}`")
		return err
	}
	chatID, _ := messageTarget(update)
	name := a.ActiveRepo(chatID)
	if name == "" {
		_, err := a.replyMarkdown(ctx, update, config.NoActiveRepoMessage)
		return err
	}
	runGeneration := a.Generation(name)
	lockCtx, release, err := a.Locks.Acquire(ctx, name)
	if err != nil {
		return err
	}
	defer release()
	if a.Generation(name) != runGeneration {
		_, err := a.replyMarkdown(lockCtx, update, fmt.Sprintf("Cancelled the queued `/sh` command for `%s`.", name))
		return err
	}
	shell, err := a.GetShellContainer(lockCtx)
	if err != nil || shell == nil {
		_, err := a.replyMarkdown(lockCtx, update, fmt.Sprintf("Shell container for `%s` is not available.", name))
		return err
	}
	_ = a.Telegram.SendChatAction(lockCtx, chatID, "typing")
	output, err := a.ExecuteCancellable(lockCtx, shell, command, chatID, name, repoWorkspace(name))
	if err != nil {
		if err == ErrRunCancelled {
			_, replyErr := a.replyMarkdown(lockCtx, update, fmt.Sprintf("Cancelled `/sh %s` in `%s`.", command, name))
			return replyErr
		}
		return err
	}
	_, err = a.SendResponse(lockCtx, chatID, update.Message.ID, output, false, "sh_output", nil)
	return err
}
