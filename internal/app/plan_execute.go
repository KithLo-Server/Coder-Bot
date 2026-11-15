package app

import (
	"context"

	"opencodebot/internal/opencode"
	"opencodebot/internal/telegram"
)

// RemovePlanButton removes the inline keyboard of a plan response.
func (a *App) RemovePlanButton(ctx context.Context, chatID int64, messageID int) {
	if messageID == 0 {
		return
	}
	if err := a.Telegram.EditReplyMarkup(ctx, chatID, messageID, nil); err != nil {
		a.Logger.Printf("Could not remove the plan button from %d: %v", messageID, err)
	}
}

// HandlePlanExecuteCallback executes a plan in build mode and commits it.
func (a *App) HandlePlanExecuteCallback(ctx context.Context, update *telegram.Update) error {
	query := update.Callback
	if query == nil {
		return nil
	}
	if !a.Authorized(update) {
		return a.Telegram.AnswerCallback(ctx, query.ID, "Not authorized.", false)
	}
	runID, ok := opencode.CallbackPayload(query.Data, planExecutePrefix)
	if !ok {
		return a.Telegram.AnswerCallback(ctx, query.ID, "Unknown action.", false)
	}
	chatID, _ := messageTarget(update)
	if query.Message != nil {
		a.RemovePlanButton(ctx, chatID, query.Message.ID)
	}
	consumed := a.PopPendingPlan(chatID, runID)
	if !consumed {
		return a.Telegram.AnswerCallback(ctx, query.ID, "This plan is no longer pending; run /ask again.", true)
	}
	if err := a.Telegram.AnswerCallback(ctx, query.ID, "Executing plan...", false); err != nil {
		return err
	}
	message := query.Message
	if message == nil {
		return nil
	}
	return a.RunOpencodeTaskOnMessage(ctx, chatID, message, planExecutePrompt, "build", "build_response", "")
}
