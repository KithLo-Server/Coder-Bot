package app

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"opencodebot/internal/config"
	"opencodebot/internal/opencode"
	"opencodebot/internal/telegram"
)

const (
	opencodeMaxAttempts = 3
	planExecuteButton   = "🚀 Execute Plan & Commit"
	planExecutePrompt   = "Execute the plan and git commit"
	planExecutePrefix   = "planexec:"
	modelMenuPrefix     = "modelmenu:"
	commandSetPrefix    = "cmdset:"
)

var opencodeRetryDelay = time.Second

func (a *App) reportCancelled(ctx context.Context, chatID int64, message *telegram.Message, status *telegram.Message, name string) {
	a.DeleteMessageBestEffort(ctx, status.ChatID, status.ID)
	_, _ = a.Telegram.SendMessage(ctx, chatID, fmt.Sprintf("Cancelled the OpenCode run in `%s`.", name), telegram.SendOptions{Markdown: true, ReplyTo: message.ID})
}

// RunOpencodeTaskOnMessage runs OpenCode and replies with the clean result.
func (a *App) RunOpencodeTaskOnMessage(ctx context.Context, chatID int64, message *telegram.Message, prompt, mode, filenameBase, repoName string) error {
	name := repoName
	if name == "" {
		name = a.ActiveRepo(chatID)
	}
	if name == "" || !a.RepoExists(name) {
		_, err := a.Telegram.SendMessage(ctx, chatID, config.NoActiveRepoMessage, telegram.SendOptions{Markdown: true, ReplyTo: message.ID})
		return err
	}
	runGeneration := a.Generation(name)
	lockCtx, release, err := a.Locks.Acquire(ctx, name)
	if err != nil {
		return err
	}
	defer release()

	if a.Generation(name) != runGeneration {
		_, err := a.Telegram.SendMessage(lockCtx, chatID, fmt.Sprintf("Cancelled the queued OpenCode run for `%s`.", name), telegram.SendOptions{Markdown: true, ReplyTo: message.ID})
		return err
	}
	container, err := a.GetRepoContainer(lockCtx, name)
	if err != nil || container == nil {
		_, err := a.Telegram.SendMessage(lockCtx, chatID, config.NoActiveRepoMessage, telegram.SendOptions{Markdown: true, ReplyTo: message.ID})
		return err
	}
	a.RemovePendingPlanButton(lockCtx, chatID)
	_ = a.Telegram.SendChatAction(lockCtx, chatID, "typing")
	status, err := a.Telegram.SendMessage(lockCtx, chatID, fmt.Sprintf("Running OpenCode in *%s* mode...", strings.ToUpper(mode)), telegram.SendOptions{Markdown: true, ReplyTo: message.ID})
	if err != nil {
		return err
	}

	sessionID := a.Session(chatID, name)
	variant := a.VariantForRepo(name)
	variantValue := ""
	if variant != nil {
		variantValue = *variant
	}
	model := a.ModelForRepo(name)

	rawOutput := ""
	attempt := 0
	for attempt = 1; attempt <= opencodeMaxAttempts; attempt++ {
		command := opencode.BuildCommand(prompt, mode, sessionID, variantValue, model, a.Cfg.Catalog.DefaultID)
		rawOutput, err = a.ExecuteCancellable(lockCtx, container, command, chatID, name, config.WorkspaceDir)
		if err == ErrRunCancelled {
			a.reportCancelled(lockCtx, chatID, message, status, name)
			return nil
		}
		if err != nil {
			return err
		}
		if !opencode.FailedWithoutText(rawOutput) {
			break
		}
		a.Logger.Printf("OpenCode run for chat %d in repo %s failed on attempt %d/%d: %s", chatID, name, attempt, opencodeMaxAttempts, opencode.Response(rawOutput))
		if attempt >= opencodeMaxAttempts {
			break
		}
		if a.Generation(name) != runGeneration {
			a.reportCancelled(lockCtx, chatID, message, status, name)
			return nil
		}
		a.editStatus(lockCtx, status, fmt.Sprintf("OpenCode failed (attempt %d/%d); retrying...", attempt, opencodeMaxAttempts))
		select {
		case <-lockCtx.Done():
			return lockCtx.Err()
		case <-time.After(opencodeRetryDelay * time.Duration(attempt)):
		}
		if a.Generation(name) != runGeneration {
			a.reportCancelled(lockCtx, chatID, message, status, name)
			return nil
		}
	}

	cleanOutput := opencode.Response(rawOutput)
	failed := opencode.FailedWithoutText(rawOutput)
	if failed {
		if err := a.ForgetSession(chatID, name); err != nil {
			a.Logger.Printf("Failed to clear session for chat %d in %s: %v", chatID, name, err)
		}
		cleanOutput = fmt.Sprintf("%s\n\nOpenCode failed after %d attempt(s). The session was reset; try the prompt again.", cleanOutput, attempt)
		a.Logger.Printf("OpenCode run for chat %d in repo %s failed after %d attempt(s)", chatID, name, attempt)
	} else {
		newSessionID := opencode.SessionID(rawOutput)
		if newSessionID != "" {
			if err := a.SetSession(chatID, name, newSessionID); err != nil {
				a.Logger.Printf("Failed to store session for chat %d in %s: %v", chatID, name, err)
			}
		} else {
			if sessionID != "" {
				if err := a.ForgetSession(chatID, name); err != nil {
					a.Logger.Printf("Failed to clear session for chat %d in %s: %v", chatID, name, err)
				}
			}
			a.Logger.Printf("OpenCode run for chat %d in repo %s returned no session id", chatID, name)
		}
	}
	a.DeleteMessageBestEffort(lockCtx, status.ChatID, status.ID)

	if mode != "plan" {
		base := filenameBase
		if base == "" {
			base = mode + "_response"
		}
		_, err := a.SendResponse(lockCtx, chatID, message.ID, cleanOutput, true, base, nil)
		return err
	}

	token := a.NewPlanToken()
	keyboard := telegram.Keyboard{{{Text: planExecuteButton, Data: planExecutePrefix + token}}}
	base := filenameBase
	if base == "" {
		base = mode + "_response"
	}
	if utf8.RuneCountInString(cleanOutput) > config.MaxMessageLength {
		if _, err := a.SendResponse(lockCtx, chatID, message.ID, cleanOutput, true, base, nil); err != nil {
			return err
		}
		followup, err := a.Telegram.SendMessage(lockCtx, chatID, "The plan is attached above. Execute it below.", telegram.SendOptions{ReplyTo: message.ID, Keyboard: keyboard})
		if err != nil {
			return err
		}
		a.RegisterPendingPlan(chatID, followup.ID, token)
		return nil
	}
	sent, err := a.SendResponse(lockCtx, chatID, message.ID, cleanOutput, true, base, keyboard)
	if err != nil {
		return err
	}
	if sent == nil || sent.ID == 0 {
		a.DropPendingPlan(chatID)
		return nil
	}
	a.RegisterPendingPlan(chatID, sent.ID, token)
	return nil
}

// RunOpencodeTask is the update-based wrapper used by text commands.
func (a *App) RunOpencodeTask(ctx context.Context, update *telegram.Update, prompt, mode string) error {
	chatID, _ := messageTarget(update)
	return a.RunOpencodeTaskOnMessage(ctx, chatID, update.Message, prompt, mode, "", "")
}

func (a *App) runPrompt(ctx context.Context, update *telegram.Update, args []string, mode, usage string) error {
	if !a.Authorized(update) {
		return nil
	}
	prompt := strings.Join(args, " ")
	if prompt == "" {
		_, err := a.replyMarkdown(ctx, update, usage)
		return err
	}
	return a.RunOpencodeTask(ctx, update, prompt, mode)
}

// CmdAsk implements /ask.
func (a *App) CmdAsk(ctx context.Context, update *telegram.Update, args []string) error {
	return a.runPrompt(ctx, update, args, "plan", "Usage: `/ask {prompt}`")
}

// CmdCode implements /code.
func (a *App) CmdCode(ctx context.Context, update *telegram.Update, args []string) error {
	return a.runPrompt(ctx, update, args, "build", "Usage: `/code {prompt}`")
}

func (a *App) thinkUsage(repoName string) string {
	modelID := a.ModelForRepo(repoName)
	label := a.Cfg.Catalog.Label(modelID)
	variants := a.Cfg.Catalog.Variants(modelID)
	current := a.VariantForRepo(repoName)
	lines := []string{fmt.Sprintf("Model: **%s** (`%s`)", label, modelID)}
	if len(variants) > 0 {
		lines = append(lines, "Variants:")
		for index, variant := range variants {
			lines = append(lines, fmt.Sprintf("%d. `%s`", index+1, variant))
		}
	} else {
		lines = append(lines, "No variants are configured for this model.")
	}
	if current != nil {
		lines = append(lines, fmt.Sprintf("Current variant: `%s`", *current))
	} else {
		lines = append(lines, "Current variant: none")
	}
	lines = append(lines, "Usage: `/think <number|variant>` or `/think off`.")
	return strings.Join(lines, "\n")
}

// CmdThink implements /think.
func (a *App) CmdThink(ctx context.Context, update *telegram.Update, args []string) error {
	if !a.Authorized(update) {
		return nil
	}
	chatID, _ := messageTarget(update)
	repoName := a.ActiveRepo(chatID)
	if repoName == "" {
		_, err := a.replyMarkdown(ctx, update, config.NoActiveRepoMessage)
		return err
	}
	modelID := a.ModelForRepo(repoName)
	usage := a.thinkUsage(repoName)
	if len(args) == 0 {
		_, err := a.replyMarkdown(ctx, update, usage)
		return err
	}
	accepted, variant := a.ResolveVariantChoice(modelID, strings.TrimSpace(strings.Join(args, " ")))
	if !accepted {
		_, err := a.replyMarkdown(ctx, update, usage)
		return err
	}
	if err := a.SetRepoSelection(repoName, modelID, variant); err != nil {
		return err
	}
	if variant == nil {
		_, err := a.replyMarkdown(ctx, update, "Thinking is now `off`; OpenCode uses the model's default variant.")
		return err
	}
	_, err := a.replyMarkdown(ctx, update, fmt.Sprintf("Thinking variant set to `%s` for subsequent OpenCode runs.", *variant))
	return err
}

// CmdModel implements /model.
func (a *App) CmdModel(ctx context.Context, update *telegram.Update, args []string) error {
	return a.StartModelSelection(ctx, update)
}

// CmdClear implements /clear.
func (a *App) CmdClear(ctx context.Context, update *telegram.Update, args []string) error {
	if !a.Authorized(update) {
		return nil
	}
	chatID, _ := messageTarget(update)
	name := a.ActiveName(chatID)
	sessionReset := false
	var repoLabel string
	if name != "" {
		lockCtx, release, err := a.Locks.Acquire(ctx, name)
		if err != nil {
			return err
		}
		a.RemovePendingPlanButton(lockCtx, chatID)
		sessionReset = a.Session(chatID, name) != ""
		forgetErr := a.ForgetSession(chatID, name)
		release()
		if forgetErr != nil {
			return forgetErr
		}
		repoLabel = fmt.Sprintf("`%s`", name)
	} else {
		a.RemovePendingPlanButton(ctx, chatID)
		sessionReset = a.ChatHasSessions(chatID)
		if err := a.ForgetChatSessions(chatID); err != nil {
			return err
		}
		repoLabel = "this chat"
	}
	var message string
	if sessionReset {
		message = fmt.Sprintf("Context cleared for %s. The next OpenCode run starts a new session.", repoLabel)
	} else {
		message = fmt.Sprintf("There is no OpenCode context to clear for %s.", repoLabel)
	}
	_, err := a.replyMarkdown(ctx, update, message)
	return err
}

// HandlePhoto marks up an uploaded photo and asks OpenCode to inspect it.
func (a *App) HandlePhoto(ctx context.Context, update *telegram.Update, args []string) error {
	if !a.Authorized(update) || update.Message == nil {
		return nil
	}
	chatID, _ := messageTarget(update)
	name := a.ActiveRepo(chatID)
	if name == "" {
		_, err := a.replyMarkdown(ctx, update, config.NoActiveRepoMessage)
		return err
	}
	if len(update.Message.Photo) == 0 {
		return nil
	}
	photo := update.Message.Photo[len(update.Message.Photo)-1]
	caption := strings.TrimSpace(update.Message.Caption)
	mode := "plan"
	prompt := caption
	if strings.HasPrefix(prompt, "/code") {
		mode = "build"
		prompt = strings.TrimSpace(prompt[len("/code"):])
	} else if strings.HasPrefix(prompt, "/ask") {
		prompt = strings.TrimSpace(prompt[len("/ask"):])
	}
	if prompt == "" {
		prompt = "Please inspect and analyze this image in the context of this repository."
	}

	lockCtx, release, err := a.Locks.Acquire(ctx, name)
	if err != nil {
		return err
	}
	defer release()
	baseContainer := a.BaseContainer()
	_ = a.Telegram.SendChatAction(lockCtx, chatID, "typing")
	status, err := a.replyMarkdown(lockCtx, update, "Uploading image to the upload volume...")
	if err != nil {
		return err
	}
	data, _, err := a.Telegram.GetFileBytes(lockCtx, photo.FileID)
	if err != nil {
		a.editStatus(lockCtx, status, fmt.Sprintf("Failed to process image: `%v`", err))
		return nil
	}
	if err := EnsureContainerDirectory(lockCtx, baseContainer, a.Cfg.UploadsDir); err != nil {
		a.editStatus(lockCtx, status, fmt.Sprintf("Failed to process image: `%v`", err))
		return nil
	}
	filename := TelegramUploadName(".jpg")
	if err := CopyToContainer(lockCtx, baseContainer, data, filename, a.Cfg.UploadsDir); err != nil {
		a.editStatus(lockCtx, status, fmt.Sprintf("Failed to process image: `%v`", err))
		return nil
	}
	imagePath := path.Join(a.Cfg.UploadsDir, filename)
	a.editStatus(lockCtx, status, fmt.Sprintf("Image saved as `%s`. Running OpenCode in *%s* mode...", imagePath, strings.ToUpper(mode)))
	fullPrompt := fmt.Sprintf("Inspect the uploaded image at '%s'. %s", imagePath, prompt)
	a.DeleteMessageBestEffort(lockCtx, status.ChatID, status.ID)
	return a.RunOpencodeTaskOnMessage(lockCtx, chatID, update.Message, fullPrompt, mode, "image_inspection", name)
}

// HandleDefaultMessage runs plain text as plan mode and #text as build mode.
func (a *App) HandleDefaultMessage(ctx context.Context, update *telegram.Update, args []string) error {
	if update.Message == nil || update.Message.Text == "" {
		return nil
	}
	if strings.HasPrefix(update.Message.Text, "/") {
		return nil
	}
	handled, err := a.HandleCommandEntryText(ctx, update)
	if err != nil || handled {
		return err
	}
	if !a.Authorized(update) {
		return nil
	}
	prompt := strings.TrimSpace(update.Message.Text)
	mode := "plan"
	if strings.HasPrefix(prompt, "#") {
		mode = "build"
		prompt = strings.TrimSpace(prompt[1:])
		if prompt == "" {
			_, err := a.replyMarkdown(ctx, update, "Usage: send `# {prompt}` to run OpenCode in build mode.")
			return err
		}
	}
	return a.RunOpencodeTask(ctx, update, prompt, mode)
}
