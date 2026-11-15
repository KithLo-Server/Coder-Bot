package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"opencodebot/internal/opencode"
	"opencodebot/internal/telegram"
)

const (
	stageName     = "name"
	stageProvider = "provider"
	stageModel    = "model"
	stageVariant  = "variant"
)

const modelCancelledText = "Model selection cancelled."

func modelCallbackData(token, stage string, index int) string {
	return fmt.Sprintf("%s%s:%s:%d", modelMenuPrefix, token, stage, index)
}

func modelKeyboard(token, stage string, labels []string) telegram.Keyboard {
	keyboard := make(telegram.Keyboard, 0, len(labels))
	for index, label := range labels {
		keyboard = append(keyboard, []telegram.Button{{Text: label, Data: modelCallbackData(token, stage, index)}})
	}
	return keyboard
}

func (a *App) currentModelLine(repoName string) string {
	modelID := a.ModelForRepo(repoName)
	label := a.Cfg.Catalog.Label(modelID)
	line := fmt.Sprintf("Current: **%s** (`%s`)", label, modelID)
	if variant := a.VariantForRepo(repoName); variant != nil {
		line += fmt.Sprintf(", variant `%s`", *variant)
	}
	return line
}

func (a *App) modelPromptText(repoName string, pending *ModelPrompt) string {
	catalog := a.Cfg.Catalog
	var name, provider, modelID string
	if pending.HasName && pending.NameIndex >= 0 && pending.NameIndex < len(catalog.Names) {
		name = catalog.Names[pending.NameIndex]
	}
	if name != "" {
		if group := catalog.ByName[name]; group != nil {
			if pending.HasProvider && pending.ProviderIndex >= 0 && pending.ProviderIndex < len(group.Names) {
				provider = group.Names[pending.ProviderIndex]
			}
		}
	}
	if provider != "" {
		if models := catalog.ByName[name].ByName[provider]; models != nil {
			if pending.HasModel && pending.ModelIndex >= 0 && pending.ModelIndex < len(models.IDs) {
				modelID = models.IDs[pending.ModelIndex]
			}
		}
	}

	lines := []string{fmt.Sprintf("**Select model for `%s`**", repoName), a.currentModelLine(repoName), ""}
	switch pending.Stage {
	case stageName:
		lines = append(lines, "Choose a name:")
	case stageProvider:
		lines = append(lines, fmt.Sprintf("**%s**", name), "Choose a provider:")
	case stageModel:
		lines = append(lines, fmt.Sprintf("**%s** / **%s**", name, provider), "Choose a model:")
	default:
		lines = append(lines, fmt.Sprintf("**%s** / **%s** / **%s**", name, provider, a.Cfg.Catalog.Label(modelID)), "Choose a variant:")
	}
	return strings.Join(lines, "\n")
}

func (a *App) modelStageLabels(pending *ModelPrompt) []string {
	catalog := a.Cfg.Catalog
	switch pending.Stage {
	case stageName:
		return append([]string(nil), catalog.Names...)
	case stageProvider:
		name := catalog.Names[pending.NameIndex]
		return append([]string(nil), catalog.ByName[name].Names...)
	case stageModel:
		name := catalog.Names[pending.NameIndex]
		group := catalog.ByName[name]
		provider := group.Names[pending.ProviderIndex]
		models := group.ByName[provider]
		labels := make([]string, len(models.IDs))
		for index, modelID := range models.IDs {
			labels[index] = models.ByID[modelID].Name
		}
		return labels
	default:
		name := catalog.Names[pending.NameIndex]
		group := catalog.ByName[name]
		provider := group.Names[pending.ProviderIndex]
		models := group.ByName[provider]
		modelID := models.IDs[pending.ModelIndex]
		return catalog.Variants(modelID)
	}
}

func (a *App) modelNextStageKeyboard(pending *ModelPrompt) telegram.Keyboard {
	return modelKeyboard(pending.Token, pending.Stage, a.modelStageLabels(pending))
}

// StartModelSelection starts the interactive /model prompt.
func (a *App) StartModelSelection(ctx context.Context, update *telegram.Update) error {
	if !a.Authorized(update) {
		return nil
	}
	chatID, _ := messageTarget(update)
	repoName := a.ActiveRepo(chatID)
	if repoName == "" {
		_, err := a.replyMarkdown(ctx, update, "No active repository. Use `/create` or `/set` first.")
		return err
	}
	a.CancelModelSelection(ctx, chatID, modelCancelledText)
	pending := &ModelPrompt{
		Token:     a.NewModelToken(),
		ChatID:    chatID,
		Repo:      repoName,
		Stage:     stageName,
		NameIndex: -1,
	}
	sent, err := a.Telegram.SendMessage(ctx, chatID, a.modelPromptText(repoName, pending), telegram.SendOptions{
		Markdown: true,
		Keyboard: a.modelNextStageKeyboard(pending),
		ReplyTo:  update.Message.ID,
	})
	if err != nil {
		return err
	}
	pending.MessageID = sent.ID
	a.SetModelPrompt(chatID, pending)
	return nil
}

// CancelModelSelection edits an open /model prompt to say it was cancelled.
func (a *App) CancelModelSelection(ctx context.Context, chatID int64, message string) bool {
	pending := a.PopModelPrompt(chatID)
	if pending == nil {
		return false
	}
	if pending.MessageID == 0 {
		return true
	}
	if err := a.Telegram.EditMessageText(ctx, chatID, pending.MessageID, message, telegram.SendOptions{}); err != nil {
		a.Logger.Printf("Could not update the cancelled /model prompt: %v", err)
		_ = a.Telegram.EditReplyMarkup(ctx, chatID, pending.MessageID, nil)
	}
	return true
}

// CancelPendingModelPrompt cancels an open prompt when any new message arrives.
func (a *App) CancelPendingModelPrompt(ctx context.Context, update *telegram.Update) {
	if update == nil || update.Message == nil {
		return
	}
	chatID, _ := messageTarget(update)
	if a.HasModelPrompt(chatID) {
		a.CancelModelSelection(ctx, chatID, modelCancelledText)
	}
}

func parseModelCallback(data string) (string, string, int, bool) {
	payload, ok := opencode.CallbackPayload(data, modelMenuPrefix)
	if !ok {
		return "", "", 0, false
	}
	parts := strings.Split(payload, ":")
	if len(parts) != 3 {
		return "", "", 0, false
	}
	index, err := strconv.Atoi(parts[2])
	if err != nil {
		return "", "", 0, false
	}
	return parts[0], parts[1], index, true
}

func (a *App) finalizeModel(ctx context.Context, update *telegram.Update, pending *ModelPrompt, modelID string, variant *string) error {
	repoName := pending.Repo
	a.SetRepoSelection(repoName, modelID, variant)
	a.Save()
	a.PopModelPrompt(pending.ChatID)
	label := a.Cfg.Catalog.Label(modelID)
	text := fmt.Sprintf("Model for `%s` set to **%s** (`%s`)", repoName, label, modelID)
	if variant != nil {
		text += fmt.Sprintf(" with variant `%s`", *variant)
	}
	text += "."
	chatID, _ := messageTarget(update)
	messageID := 0
	if update.Callback != nil && update.Callback.Message != nil {
		messageID = update.Callback.Message.ID
	}
	if err := a.Telegram.EditMessageText(ctx, chatID, messageID, text, telegram.SendOptions{Markdown: true}); err != nil {
		return err
	}
	return a.Telegram.AnswerCallback(ctx, update.Callback.ID, "", false)
}

func (a *App) autoAdvanceModel(pending *ModelPrompt) string {
	catalog := a.Cfg.Catalog
	for {
		switch pending.Stage {
		case stageProvider:
			group := catalog.ByName[catalog.Names[pending.NameIndex]]
			if len(group.Names) != 1 {
				return ""
			}
			pending.ProviderIndex = 0
			pending.HasProvider = true
			pending.Stage = stageModel
		case stageModel:
			name := catalog.Names[pending.NameIndex]
			group := catalog.ByName[name]
			provider := group.Names[pending.ProviderIndex]
			models := group.ByName[provider]
			if len(models.IDs) != 1 {
				return ""
			}
			modelID := models.IDs[0]
			pending.ModelIndex = 0
			pending.HasModel = true
			if len(catalog.Variants(modelID)) > 0 {
				pending.Stage = stageVariant
				return ""
			}
			return modelID
		default:
			return ""
		}
	}
}

// HandleModelCallback walks the NAME -> PROVIDER -> model -> variant flow.
func (a *App) HandleModelCallback(ctx context.Context, update *telegram.Update) error {
	query := update.Callback
	if query == nil {
		return nil
	}
	if !a.Authorized(update) {
		return a.Telegram.AnswerCallback(ctx, query.ID, "Not authorized.", false)
	}
	token, stage, index, ok := parseModelCallback(query.Data)
	if !ok {
		return a.Telegram.AnswerCallback(ctx, query.ID, "Unknown action.", false)
	}
	chatID, _ := messageTarget(update)
	pending := a.GetModelPrompt(chatID)
	if pending == nil || pending.Token != token {
		return a.Telegram.AnswerCallback(ctx, query.ID, "This model selection expired. Run /model again.", true)
	}
	if pending.Stage != stage {
		return a.Telegram.AnswerCallback(ctx, query.ID, "This step is no longer active.", false)
	}
	catalog := a.Cfg.Catalog
	finalModelID := ""

	switch stage {
	case stageName:
		if index < 0 || index >= len(catalog.Names) {
			return a.Telegram.AnswerCallback(ctx, query.ID, "That name is no longer available.", false)
		}
		pending.NameIndex = index
		pending.HasName = true
		pending.HasProvider = false
		pending.HasModel = false
		pending.Stage = stageProvider
		finalModelID = a.autoAdvanceModel(pending)
	case stageProvider:
		if !pending.HasName {
			return a.Telegram.AnswerCallback(ctx, query.ID, "This selection expired. Run /model again.", true)
		}
		group := catalog.ByName[catalog.Names[pending.NameIndex]]
		if index < 0 || index >= len(group.Names) {
			return a.Telegram.AnswerCallback(ctx, query.ID, "That provider is no longer available.", false)
		}
		pending.ProviderIndex = index
		pending.HasProvider = true
		pending.HasModel = false
		pending.Stage = stageModel
		finalModelID = a.autoAdvanceModel(pending)
	case stageModel:
		if !pending.HasName || !pending.HasProvider {
			return a.Telegram.AnswerCallback(ctx, query.ID, "This selection expired. Run /model again.", true)
		}
		name := catalog.Names[pending.NameIndex]
		group := catalog.ByName[name]
		provider := group.Names[pending.ProviderIndex]
		models := group.ByName[provider]
		if index < 0 || index >= len(models.IDs) {
			return a.Telegram.AnswerCallback(ctx, query.ID, "That model is no longer available.", false)
		}
		pending.ModelIndex = index
		pending.HasModel = true
		modelID := models.IDs[index]
		if len(catalog.Variants(modelID)) == 0 {
			return a.finalizeModel(ctx, update, pending, modelID, nil)
		}
		pending.Stage = stageVariant
	default:
		if !pending.HasName || !pending.HasProvider || !pending.HasModel {
			return a.Telegram.AnswerCallback(ctx, query.ID, "This selection expired. Run /model again.", true)
		}
		name := catalog.Names[pending.NameIndex]
		group := catalog.ByName[name]
		provider := group.Names[pending.ProviderIndex]
		models := group.ByName[provider]
		modelID := models.IDs[pending.ModelIndex]
		variants := catalog.Variants(modelID)
		if index < 0 || index >= len(variants) {
			return a.Telegram.AnswerCallback(ctx, query.ID, "That variant is no longer available.", false)
		}
		variant := variants[index]
		return a.finalizeModel(ctx, update, pending, modelID, &variant)
	}

	if finalModelID != "" {
		return a.finalizeModel(ctx, update, pending, finalModelID, nil)
	}
	if !a.ReplaceModelPrompt(chatID, token, pending) {
		return a.Telegram.AnswerCallback(ctx, query.ID, "This model selection expired. Run /model again.", true)
	}
	if err := a.Telegram.AnswerCallback(ctx, query.ID, "", false); err != nil {
		return err
	}
	messageID := 0
	if query.Message != nil {
		messageID = query.Message.ID
	}
	return a.Telegram.EditMessageText(ctx, chatID, messageID, a.modelPromptText(pending.Repo, pending), telegram.SendOptions{
		Markdown: true,
		Keyboard: a.modelNextStageKeyboard(pending),
	})
}
