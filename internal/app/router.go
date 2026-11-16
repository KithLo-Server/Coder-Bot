package app

import (
	"context"
	"regexp"
	"strings"

	"opencodebot/internal/telegram"
)

var commandHandlers = map[string]HandlerFunc{
	"create":    (*App).CmdCreate,
	"set":       (*App).CmdSet,
	"info":      (*App).CmdInfo,
	"list":      (*App).CmdList,
	"mount":     (*App).CmdMount,
	"unmount":   (*App).CmdUnmount,
	"stop":      (*App).CmdStop,
	"purge":     (*App).CmdPurge,
	"reload":    (*App).CmdReload,
	"reloadall": (*App).CmdReloadAll,
	"upgrade":   (*App).CmdUpgrade,
	"dl":        (*App).CmdDL,
	"ul":        (*App).CmdUL,
	"sh":        (*App).CmdSh,
	"pull":      (*App).CmdPull,
	"push":      (*App).CmdPush,
	"ask":       (*App).CmdAsk,
	"code":      (*App).CmdCode,
	"think":     (*App).CmdThink,
	"model":     (*App).CmdModel,
	"clear":     (*App).CmdClear,
	"cancel":    (*App).CmdCancel,
	"cmd":       (*App).CmdCmd,
	"exec":      (*App).CmdExec,
}

var ulCaptionRouterPattern = regexp.MustCompile(`(?is)^/ul(?:@\w+)?(?:\s|$)`)

func parseCommand(text string) (string, []string, bool) {
	if !strings.HasPrefix(text, "/") {
		return "", nil, false
	}
	trimmed := strings.TrimSpace(text[1:])
	first, rest, _ := strings.Cut(trimmed, " ")
	if before, _, ok := strings.Cut(first, "@"); ok {
		first = before
	}
	first = strings.ToLower(first)
	if first == "" {
		return "", nil, false
	}
	return first, strings.Fields(rest), true
}

// Handle routes one Telegram update through the handler ordering the Python bot used.
func (a *App) Handle(ctx context.Context, update *telegram.Update) {
	if update == nil {
		return
	}
	if update.Callback != nil {
		var err error
		switch {
		case strings.HasPrefix(update.Callback.Data, modelMenuPrefix):
			err = a.HandleModelCallback(ctx, update)
		case strings.HasPrefix(update.Callback.Data, commandSetPrefix):
			err = a.HandleCommandCallback(ctx, update)
		case strings.HasPrefix(update.Callback.Data, planExecutePrefix):
			err = a.HandlePlanExecuteCallback(ctx, update)
		}
		if err != nil {
			a.Logger.Printf("Callback handler failed: %v", err)
		}
		return
	}
	if update.Message == nil {
		return
	}

	// Group -2: any new message cancels an open /model prompt.
	a.CancelPendingModelPrompt(ctx, update)

	message := update.Message
	if strings.HasPrefix(message.Text, "/") {
		name, args, ok := parseCommand(message.Text)
		if !ok {
			return
		}
		// Group -1: capture typed commands while a /cmd set is being collected.
		chatID, _ := messageTarget(update)
		if a.HasPendingCommand(chatID) {
			handled, err := a.HandleCommandEntryText(ctx, update)
			if err != nil {
				a.Logger.Printf("Command-set capture failed: %v", err)
			}
			if handled {
				return
			}
		}
		if handler, exists := commandHandlers[name]; exists {
			if err := handler(a, ctx, update, args); err != nil {
				a.Logger.Printf("Handler /%s failed: %v", name, err)
			}
		}
		return
	}

	// Attachment captions carrying /ul stop later groups.
	if message.Caption != "" && ulCaptionRouterPattern.MatchString(strings.TrimSpace(message.Caption)) {
		_, args, _ := parseCommand(strings.TrimSpace(message.Caption))
		if err := a.CmdUL(ctx, update, args); err != nil {
			a.Logger.Printf("Handler /ul failed: %v", err)
		}
		return
	}
	if len(message.Photo) > 0 {
		if err := a.HandlePhoto(ctx, update, nil); err != nil {
			a.Logger.Printf("Photo handler failed: %v", err)
		}
		return
	}
	if message.Text != "" {
		if err := a.HandleDefaultMessage(ctx, update, nil); err != nil {
			a.Logger.Printf("Default message handler failed: %v", err)
		}
	}
}
