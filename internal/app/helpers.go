package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"opencodebot/internal/config"
	"opencodebot/internal/opencode"
	"opencodebot/internal/telegram"
)

// Authorized reports whether the update's user may use the bot.
func (a *App) Authorized(update *telegram.Update) bool {
	if len(a.Cfg.AllowedUserIDs) == 0 {
		return true
	}
	if update == nil {
		return false
	}
	return a.Cfg.AllowedUserIDs[strconv.FormatInt(update.UserID, 10)]
}

// DeleteMessageBestEffort removes a status message, ignoring failures.
func (a *App) DeleteMessageBestEffort(ctx context.Context, chatID int64, messageID int) {
	if messageID == 0 {
		return
	}
	_ = a.Telegram.DeleteMessage(ctx, chatID, messageID)
}

// SendResponse delivers text inline, as Markdown, or as a document when oversized.
func (a *App) SendResponse(ctx context.Context, chatID int64, replyTo int, text string, markdown bool, filenameBase string, keyboard telegram.Keyboard) (*telegram.Message, error) {
	if filenameBase == "" {
		filenameBase = "output"
	}
	if strings.TrimSpace(text) == "" {
		a.Logger.Printf("Outgoing response to chat %d: (Empty response)", chatID)
		return a.Telegram.SendMessage(ctx, chatID, "(Empty response)", telegram.SendOptions{ReplyTo: replyTo})
	}

	length := utf8.RuneCountInString(text)
	if length > config.MaxMessageLength {
		extension := ".txt"
		if markdown {
			extension = ".md"
		}
		filename := filenameBase + extension
		caption := fmt.Sprintf("Response was %s characters. Attached as `%s`.", commaInt(length), filename)
		a.Logger.Printf("Outgoing response to chat %d: Uploading document %q (%d chars)", chatID, filename, length)
		return a.Telegram.SendDocument(ctx, chatID, filename, []byte(text), caption, telegram.SendOptions{Markdown: true, ReplyTo: replyTo})
	}

	options := telegram.SendOptions{Markdown: markdown, Keyboard: keyboard, ReplyTo: replyTo}
	if markdown {
		a.Logger.Printf("Outgoing response to chat %d: Inline Markdown (%d chars)", chatID, length)
		sent, err := a.Telegram.SendMessage(ctx, chatID, text, options)
		if err == nil {
			return sent, nil
		}
		if !telegram.IsBadRequest(err) {
			return nil, err
		}
		a.Logger.Printf("Markdown parsing failed (%v). Falling back to plain text for chat %d", err, chatID)
		options.Markdown = false
		return a.Telegram.SendMessage(ctx, chatID, text, options)
	}

	a.Logger.Printf("Outgoing response to chat %d: Inline Code Block (%d chars)", chatID, length)
	options.Markdown = true
	sent, err := a.Telegram.SendMessage(ctx, chatID, "```\n"+text+"\n```", options)
	if err == nil {
		return sent, nil
	}
	if !telegram.IsBadRequest(err) {
		return nil, err
	}
	options.Markdown = false
	return a.Telegram.SendMessage(ctx, chatID, text, options)
}

func commaInt(value int) string {
	raw := strconv.Itoa(value)
	if len(raw) <= 3 {
		return raw
	}
	var parts []string
	for len(raw) > 3 {
		parts = append([]string{raw[len(raw)-3:]}, parts...)
		raw = raw[:len(raw)-3]
	}
	parts = append([]string{raw}, parts...)
	return strings.Join(parts, ",")
}

// TelegramUploadName returns the timestamped filename for unnamed uploads.
func TelegramUploadName(extension string) string {
	return opencode.TelegramUploadName(extension)
}

// OpencodeEnvironment returns the environment for OpenCode processes.
func (a *App) OpencodeEnvironment() map[string]string {
	env := map[string]string{"DEEPSEEK_API_KEY": a.Cfg.DeepseekAPIKey}
	if a.Cfg.OpenRouterKey != "" {
		env["OPENROUTER_API_KEY"] = a.Cfg.OpenRouterKey
	}
	if a.Cfg.VertexProject != "" {
		env["GOOGLE_VERTEX_PROJECT"] = a.Cfg.VertexProject
		env["GOOGLE_CLOUD_PROJECT"] = a.Cfg.VertexProject
		env["GOOGLE_VERTEX_LOCATION"] = a.Cfg.VertexLocation
		env["GOOGLE_CLOUD_LOCATION"] = a.Cfg.VertexLocation
		env["VERTEX_LOCATION"] = a.Cfg.VertexLocation
	}
	if a.Cfg.CredentialsJSON != "" {
		env["GOOGLE_APPLICATION_CREDENTIALS"] = config.GoogleCredentialsPath
	}
	return env
}

// WorkerEnvironment is OpencodeEnvironment plus container-bootstrap secrets.
func (a *App) WorkerEnvironment() map[string]string {
	env := a.OpencodeEnvironment()
	if a.Cfg.CredentialsJSON != "" {
		env["GOOGLE_APPLICATION_CREDENTIALS_JSON"] = a.Cfg.CredentialsJSON
	}
	return a.addGitEnvironment(env)
}

// ShellEnvironment configures a per-repository shell without OpenCode or
// provider credentials.
func (a *App) ShellEnvironment() map[string]string {
	return a.addGitEnvironment(map[string]string{})
}

func (a *App) addGitEnvironment(env map[string]string) map[string]string {
	if a.Cfg.SSHKey != "" {
		env["OPENCODE_SSH_KEY"] = a.Cfg.SSHKey
	}
	if a.Cfg.GitUserName != "" {
		env["GIT_USER_NAME"] = a.Cfg.GitUserName
	}
	if a.Cfg.GitUserEmail != "" {
		env["GIT_USER_EMAIL"] = a.Cfg.GitUserEmail
	}
	return env
}
