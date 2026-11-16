package app

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"opencodebot/internal/state"
	"opencodebot/internal/telegram"
)

const (
	callbackEdit   = "edit"
	callbackDelete = "delete"
	callbackDone   = "done"
	callbackCancel = "cancel"
)

// BotCommands maps saved-set lines to their real command handlers.
var BotCommands map[string]HandlerFunc

func init() {
	BotCommands = map[string]HandlerFunc{
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
		"cmd":       (*App).CmdCmd,
		"exec":      (*App).CmdExec,
	}
}

func commandCallbackData(action, payload string) string {
	if payload == "" {
		return commandSetPrefix + action
	}
	return commandSetPrefix + action + ":" + payload
}

func normalizeCommandName(name string) string {
	return strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(name), "/"))
}

func renderCommandEntry(name string, commands []string, editing bool) string {
	var lines []string
	var prompt string
	if len(commands) > 0 {
		label := "Command set"
		if editing {
			label = "Editing command set"
		}
		lines = []string{fmt.Sprintf("**%s** `%s` — %d command(s) saved:\n", label, name, len(commands))}
		for index, command := range commands {
			lines = append(lines, fmt.Sprintf("%d. `%s`", index+1, command))
		}
		if editing {
			prompt = "\n\nSend the next command, or press **Done** to replace the saved set."
		} else {
			prompt = "\n\nSend the next command, or press **Done** to save the set."
		}
	} else if editing {
		lines = []string{fmt.Sprintf("**Editing command set** `%s` — existing commands will be replaced.\n", name)}
		prompt = "Send the first command, or press **Cancel**."
	} else {
		lines = []string{fmt.Sprintf("**Creating command set** `%s`.\n", name)}
		prompt = "Send the first command, or press **Cancel**."
	}
	hint := "\n\nUse `%1`, `%2`, … for `/exec` arguments and `%%` for a literal `%`."
	return strings.Join(lines, "\n") + prompt + hint
}

func commandEntryKeyboard(hasCommands bool) telegram.Keyboard {
	row := []telegram.Button{{Text: "✖️ Cancel", Data: commandCallbackData(callbackCancel, "")}}
	if hasCommands {
		row = append([]telegram.Button{{Text: "✅ Done", Data: commandCallbackData(callbackDone, "")}}, row...)
	}
	return telegram.Keyboard{row}
}

func savedCommandsKeyboard(name string) telegram.Keyboard {
	return telegram.Keyboard{{
		{Text: "✏️ Edit", Data: commandCallbackData(callbackEdit, name)},
		{Text: "🗑 Delete", Data: commandCallbackData(callbackDelete, name)},
	}}
}

func (a *App) clearCommandSetButtons(ctx context.Context, chatID int64, extra ...int) {
	ids := a.PopCommandSetMessages(chatID)
	seen := make(map[int]bool)
	for _, id := range ids {
		seen[id] = true
	}
	for _, id := range extra {
		if id != 0 {
			seen[id] = true
		}
	}
	for id := range seen {
		if err := a.Telegram.EditReplyMarkup(ctx, chatID, id, nil); err != nil {
			a.Logger.Printf("Could not clear command-set keyboard from %d: %v", id, err)
		}
	}
}

func (a *App) startCommandEntry(ctx context.Context, update *telegram.Update, name string, existing []string, editing bool) error {
	chatID, _ := messageTarget(update)
	a.SetPendingCommand(chatID, &PendingCommand{
		Name:     name,
		Commands: append([]string(nil), existing...),
		Editing:  editing,
	})
	hasCommands := len(existing) > 0
	text := renderCommandEntry(name, existing, editing)
	a.clearCommandSetButtons(ctx, chatID)

	if update.Callback != nil {
		if update.Callback.Message != nil {
			_ = a.Telegram.EditMessageText(ctx, chatID, update.Callback.Message.ID, text, telegram.SendOptions{Markdown: true})
		}
		replyText := fmt.Sprintf("Command set `%s` — continue below.", name)
		if editing {
			replyText = fmt.Sprintf("Editing `%s` — continue below.", name)
		}
		replyTo := 0
		if update.Callback.Message != nil {
			replyTo = update.Callback.Message.ID
		}
		sent, err := a.Telegram.SendMessage(ctx, chatID, replyText, telegram.SendOptions{
			Markdown: true,
			Keyboard: commandEntryKeyboard(hasCommands),
			ReplyTo:  replyTo,
		})
		if err != nil {
			return err
		}
		a.TrackCommandSetMessage(chatID, sent.ID)
		return nil
	}
	sent, err := a.Telegram.SendMessage(ctx, chatID, text, telegram.SendOptions{
		Markdown: true,
		Keyboard: commandEntryKeyboard(hasCommands),
		ReplyTo:  update.Message.ID,
	})
	if err != nil {
		return err
	}
	a.TrackCommandSetMessage(chatID, sent.ID)
	return nil
}

// CmdCmd implements /cmd.
func (a *App) CmdCmd(ctx context.Context, update *telegram.Update, args []string) error {
	if !a.Authorized(update) {
		return nil
	}
	chatID, _ := messageTarget(update)
	if len(args) == 0 {
		available := "none saved yet"
		if names := a.CommandSetNames(chatID); len(names) > 0 {
			quoted := make([]string, len(names))
			for index, name := range names {
				quoted[index] = "`" + name + "`"
			}
			available = strings.Join(quoted, ", ")
		}
		_, err := a.replyMarkdown(ctx, update, fmt.Sprintf("Usage: `/cmd {name}` — then type commands one by one.\nSets in this chat: %s", available))
		return err
	}
	name := normalizeCommandName(strings.Join(args, " "))
	if name == "" {
		_, err := a.replyMarkdown(ctx, update, "Please provide a command set name.")
		return err
	}
	if pending := a.GetPendingCommand(chatID); pending != nil && pending.Name != name {
		a.DeletePendingCommand(chatID)
	}
	if existing, ok := a.CommandSet(chatID, name); ok {
		lines := []string{fmt.Sprintf("**Saved commands for** `%s` in this chat:\n", name)}
		for index, command := range existing.Commands {
			lines = append(lines, fmt.Sprintf("%d. `%s`", index+1, command))
		}
		lines = append(lines, fmt.Sprintf("\nRun them with `/exec %s`, or edit / delete the set:", name))
		a.clearCommandSetButtons(ctx, chatID)
		sent, err := a.Telegram.SendMessage(ctx, chatID, strings.Join(lines, "\n"), telegram.SendOptions{
			Markdown: true,
			Keyboard: savedCommandsKeyboard(name),
			ReplyTo:  update.Message.ID,
		})
		if err != nil {
			return err
		}
		a.TrackCommandSetMessage(chatID, sent.ID)
		return nil
	}
	return a.startCommandEntry(ctx, update, name, nil, false)
}

func isCmdControl(command string) bool {
	first := strings.Fields(command)
	if len(first) == 0 {
		return false
	}
	name := strings.ToLower(first[0])
	if before, _, ok := strings.Cut(name, "@"); ok {
		name = before
	}
	return name == "/cancel"
}

// savedCommandHandler resolves a saved line to its bot command handler.
func savedCommandHandler(command string) (string, []string, HandlerFunc, bool) {
	name, args, ok := splitBotCommand(command)
	if !ok {
		return "", nil, nil, false
	}
	handler, exists := BotCommands[name]
	if !exists {
		return name, args, nil, false
	}
	return name, args, handler, true
}

func firstInvalidSavedCommand(commands []string) (int, string, bool) {
	for index, command := range commands {
		if _, _, _, ok := savedCommandHandler(command); !ok {
			return index, command, false
		}
	}
	return 0, "", true
}

// CaptureCollectedCommand stores a typed line while a /cmd set is being collected.
func (a *App) CaptureCollectedCommand(ctx context.Context, update *telegram.Update) (bool, error) {
	if update == nil || update.Message == nil || update.Message.Text == "" || !a.Authorized(update) {
		return false, nil
	}
	chatID, _ := messageTarget(update)
	if !a.HasPendingCommand(chatID) {
		return false, nil
	}
	return a.HandleCommandEntryText(ctx, update)
}

// HandleCommandEntryText collects typed commands for an in-progress set.
func (a *App) HandleCommandEntryText(ctx context.Context, update *telegram.Update) (bool, error) {
	if update == nil || update.Message == nil || update.Message.Text == "" || !a.Authorized(update) {
		return false, nil
	}
	chatID, _ := messageTarget(update)
	pending := a.GetPendingCommand(chatID)
	if pending == nil {
		return false, nil
	}
	command := strings.TrimSpace(update.Message.Text)
	if command == "" {
		return true, nil
	}
	if isCmdControl(command) {
		return false, nil
	}
	if _, _, _, ok := savedCommandHandler(command); !ok {
		_, err := a.replyMarkdown(ctx, update, fmt.Sprintf("Only bot commands can be saved. Use `/sh` for shell commands.\nUnsupported line: `%s`", command))
		return true, err
	}
	pending = a.AppendPendingCommand(chatID, command)
	if pending == nil {
		return false, nil
	}
	a.clearCommandSetButtons(ctx, chatID)
	sent, err := a.Telegram.SendMessage(ctx, chatID, renderCommandEntry(pending.Name, pending.Commands, pending.Editing), telegram.SendOptions{
		Markdown: true,
		Keyboard: commandEntryKeyboard(true),
		ReplyTo:  update.Message.ID,
	})
	if err != nil {
		return true, err
	}
	a.TrackCommandSetMessage(chatID, sent.ID)
	return true, nil
}

func (a *App) editCallbackMessage(ctx context.Context, update *telegram.Update, text string, markdown bool) error {
	chatID, _ := messageTarget(update)
	if update.Callback == nil || update.Callback.Message == nil {
		return nil
	}
	return a.Telegram.EditMessageText(ctx, chatID, update.Callback.Message.ID, text, telegram.SendOptions{Markdown: markdown})
}

// HandleCommandCallback handles the Edit / Delete / Done / Cancel buttons.
func (a *App) HandleCommandCallback(ctx context.Context, update *telegram.Update) error {
	query := update.Callback
	if query == nil {
		return nil
	}
	if !a.Authorized(update) {
		return a.Telegram.AnswerCallback(ctx, query.ID, "Not authorized.", false)
	}
	data, ok := opencodePayload(query.Data, commandSetPrefix)
	if !ok {
		return a.Telegram.AnswerCallback(ctx, query.ID, "Unknown action.", false)
	}
	action, payload, _ := strings.Cut(data, ":")
	chatID, _ := messageTarget(update)
	_ = a.Telegram.AnswerCallback(ctx, query.ID, "", false)
	clickedID := 0
	if query.Message != nil {
		clickedID = query.Message.ID
	}
	a.clearCommandSetButtons(ctx, chatID, clickedID)

	switch action {
	case callbackEdit:
		name := strings.TrimSpace(payload)
		if name == "" || a.CommandSetsCount(chatID) == 0 {
			return a.editCallbackMessage(ctx, update, "That command set no longer exists.", false)
		}
		if _, ok := a.CommandSet(chatID, name); !ok {
			return a.editCallbackMessage(ctx, update, "That command set no longer exists.", false)
		}
		return a.startCommandEntry(ctx, update, name, nil, true)
	case callbackDelete:
		name := strings.TrimSpace(payload)
		if name == "" {
			return a.editCallbackMessage(ctx, update, "That command set no longer exists.", false)
		}
		removed, ok := a.DeleteCommandSet(chatID, name)
		if !ok {
			return a.editCallbackMessage(ctx, update, "That command set no longer exists.", false)
		}
		if pending := a.GetPendingCommand(chatID); pending != nil && pending.Name == name {
			a.DeletePendingCommand(chatID)
		}
		return a.editCallbackMessage(ctx, update, fmt.Sprintf("**Deleted command set** `%s` from this chat (%d command(s)).", name, len(removed.Commands)), true)
	case callbackCancel:
		pending := a.GetPendingCommand(chatID)
		if pending == nil {
			return a.editCallbackMessage(ctx, update, "Nothing to cancel.", false)
		}
		a.DeletePendingCommand(chatID)
		return a.editCallbackMessage(ctx, update, fmt.Sprintf("Cancelled command set `%s`. Nothing was saved.", pending.Name), true)
	case callbackDone:
		pending := a.GetPendingCommand(chatID)
		if pending == nil {
			return a.editCallbackMessage(ctx, update, "Nothing to save. Use `/cmd {name}` to start a new set.", true)
		}
		name := pending.Name
		commands := append([]string(nil), pending.Commands...)
		if len(commands) == 0 {
			a.DeletePendingCommand(chatID)
			return a.editCallbackMessage(ctx, update, fmt.Sprintf("`%s` has no commands, so nothing was saved.", name), true)
		}
		if index, invalid, ok := firstInvalidSavedCommand(commands); !ok {
			return a.editCallbackMessage(ctx, update, fmt.Sprintf("Cannot save `%s`: line %d (`%s`) is not a supported bot command. Replace it and press **Done** again.", name, index+1, invalid), true)
		}
		a.DeletePendingCommand(chatID)
		if err := a.SaveCommandSet(chatID, &state.CommandSet{Name: name, Commands: commands}); err != nil {
			return err
		}
		lines := []string{fmt.Sprintf("**Saved command set** `%s` in this chat (%d command(s)):\n", name, len(commands))}
		for index, command := range commands {
			lines = append(lines, fmt.Sprintf("%d. `%s`", index+1, command))
		}
		lines = append(lines, fmt.Sprintf("\nRun it with `/exec %s`.", name))
		return a.editCallbackMessage(ctx, update, strings.Join(lines, "\n"), true)
	}
	return a.editCallbackMessage(ctx, update, "Unknown action.", false)
}

func opencodePayload(data, prefix string) (string, bool) {
	if !strings.HasPrefix(data, prefix) {
		return "", false
	}
	return data[len(prefix):], true
}

var placeholderPattern = regexp.MustCompile(`%%|%([1-9]\d*)`)

// SubstitutePlaceholders expands %1, %2, ... and the %% escape in one pass.
func SubstitutePlaceholders(template string, args []string) string {
	return placeholderPattern.ReplaceAllStringFunc(template, func(match string) string {
		if match == "%%" {
			return "%"
		}
		index, err := strconv.Atoi(match[1:])
		if err != nil || index <= 0 || index > len(args) {
			return match
		}
		return args[index-1]
	})
}

func splitExecTarget(args []string, sets map[string]*state.CommandSet) (string, []string) {
	fullName := normalizeCommandName(strings.Join(args, " "))
	if _, ok := sets[fullName]; ok {
		return fullName, nil
	}
	if len(args) == 0 {
		return "", nil
	}
	return normalizeCommandName(args[0]), append([]string(nil), args[1:]...)
}

func splitBotCommand(command string) (string, []string, bool) {
	if !strings.HasPrefix(command, "/") {
		return "", nil, false
	}
	trimmed := strings.TrimSpace(command[1:])
	first, rest, _ := strings.Cut(trimmed, " ")
	if before, _, ok := strings.Cut(first, "@"); ok {
		first = before
	}
	first = strings.ToLower(first)
	if strings.TrimSpace(rest) == "" {
		return first, nil, true
	}
	return first, strings.Fields(rest), true
}

// CmdExec runs every command of a saved set of this chat, in order.
func (a *App) CmdExec(ctx context.Context, update *telegram.Update, args []string) error {
	if !a.Authorized(update) {
		return nil
	}
	chatID, _ := messageTarget(update)
	sets := a.CopyCommandSets(chatID)
	if len(args) == 0 {
		available := "none saved yet"
		if len(sets) > 0 {
			var names []string
			for _, name := range a.CommandSetNames(chatID) {
				names = append(names, "`"+name+"`")
			}
			available = strings.Join(names, ", ")
		}
		_, err := a.replyMarkdown(ctx, update, fmt.Sprintf("Usage: `/exec {name} [args...]`\nSets in this chat: %s", available))
		return err
	}
	name, execArgs := splitExecTarget(args, sets)
	set, ok := sets[name]
	if !ok {
		_, err := a.replyMarkdown(ctx, update, fmt.Sprintf("Command set `%s` not found in this chat. Create it with `/cmd %s`.", name, name))
		return err
	}
	if len(set.Commands) == 0 {
		_, err := a.replyMarkdown(ctx, update, fmt.Sprintf("Command set `%s` has no commands.", name))
		return err
	}
	type preparedCommand struct {
		raw     string
		args    []string
		handler HandlerFunc
	}
	prepared := make([]preparedCommand, 0, len(set.Commands))
	for _, savedCommand := range set.Commands {
		command := SubstitutePlaceholders(savedCommand, execArgs)
		_, botArgs, handler, ok := savedCommandHandler(command)
		if !ok {
			_, err := a.replyMarkdown(ctx, update, fmt.Sprintf("Cannot execute `%s`: `%s` is not a supported bot command. Only bot commands can be saved and executed.", name, command))
			return err
		}
		prepared = append(prepared, preparedCommand{raw: savedCommand, args: botArgs, handler: handler})
	}
	repoName := a.ActiveName(chatID)
	_ = a.Telegram.SendChatAction(ctx, chatID, "typing")
	scope := ""
	if repoName != "" {
		scope = fmt.Sprintf(" in `%s`", repoName)
	}
	status, err := a.replyMarkdown(ctx, update, fmt.Sprintf("Executing `%s` (%d command(s))%s...", name, len(prepared), scope))
	if err != nil {
		return err
	}
	cancelled := false
	for _, command := range prepared {
		activeRepo := a.ActiveName(chatID)
		runGeneration := a.Generation(activeRepo)
		if err := command.handler(a, ctx, update, command.args); err != nil {
			a.Logger.Printf("Command %q of set %q failed: %v", command.raw, name, err)
		}
		if a.Generation(activeRepo) != runGeneration {
			cancelled = true
			break
		}
	}
	if cancelled {
		_ = a.Telegram.EditMessageText(ctx, status.ChatID, status.ID, fmt.Sprintf("Execution of `%s` cancelled.", name), telegram.SendOptions{Markdown: true})
		return nil
	}
	a.DeleteMessageBestEffort(ctx, status.ChatID, status.ID)
	return nil
}
