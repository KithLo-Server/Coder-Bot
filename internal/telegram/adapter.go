package telegram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"opencodebot/internal/config"
)

// Adapter implements Messenger on top of github.com/go-telegram/bot.
type Adapter struct {
	Bot      *bot.Bot
	Token    string
	FileBase string
	Client   *http.Client
}

// NewAdapter builds a Telegram bot adapter and its polling bot.
func NewAdapter(cfg *config.Config, extra ...bot.Option) (*Adapter, error) {
	options := []bot.Option{
		bot.WithHTTPClient(cfg.ReadTimeout, newHTTPClient(cfg.ConnectTimeout, cfg.PoolTimeout, cfg.WriteTimeout)),
	}
	options = append(options, extra...)
	if base := NormalizeAPIBase(cfg.TelegramAPIBase); base != "" {
		options = append(options, bot.WithServerURL(base))
	}
	api, err := bot.New(cfg.TelegramToken, options...)
	if err != nil {
		return nil, err
	}
	return &Adapter{
		Bot:      api,
		Token:    cfg.TelegramToken,
		FileBase: resolveFileBase(cfg),
		Client:   newHTTPClient(cfg.ConnectTimeout, cfg.PoolTimeout, cfg.MediaWriteTimeout),
	}, nil
}

// NormalizeAPIBase turns a PTB-style base URL into the server root the Go
// library expects. For example ".../bot" becomes "http://host:8081".
func NormalizeAPIBase(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	base = strings.TrimSuffix(base, "/bot")
	return strings.TrimRight(base, "/")
}

func resolveFileBase(cfg *config.Config) string {
	if base := strings.TrimRight(strings.TrimSpace(cfg.TelegramFileBase), "/"); base != "" {
		return base
	}
	server := NormalizeAPIBase(cfg.TelegramAPIBase)
	if server == "" {
		server = "https://api.telegram.org"
	}
	return server + "/file/bot"
}

func newHTTPClient(connect, pool, total time.Duration) *http.Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: connect}).DialContext,
		IdleConnTimeout:       pool,
		MaxIdleConnsPerHost:   8,
		ResponseHeaderTimeout: total,
	}
	return &http.Client{Timeout: total, Transport: transport}
}

// ConvertUpdate converts a library update into the bot's update type.
func ConvertUpdate(update *models.Update) *Update {
	if update == nil {
		return nil
	}
	if update.CallbackQuery != nil {
		query := update.CallbackQuery
		result := &Update{UserID: query.From.ID}
		if query.Message.Message != nil {
			result.ChatID = query.Message.Message.Chat.ID
			result.Callback = &Callback{
				ID:      query.ID,
				Data:    query.Data,
				Message: ConvertMessage(query.Message.Message),
			}
		} else {
			result.Callback = &Callback{ID: query.ID, Data: query.Data}
		}
		return result
	}
	if update.Message != nil {
		message := ConvertMessage(update.Message)
		result := &Update{ChatID: message.ChatID, Message: message}
		if update.Message.From != nil {
			result.UserID = update.Message.From.ID
		}
		return result
	}
	return nil
}

// ConvertMessage converts the library message subset used by handlers.
func ConvertMessage(message *models.Message) *Message {
	if message == nil {
		return nil
	}
	result := &Message{
		ID:      message.ID,
		ChatID:  message.Chat.ID,
		Text:    message.Text,
		Caption: message.Caption,
	}
	for _, photo := range message.Photo {
		result.Photo = append(result.Photo, PhotoSize{FileID: photo.FileID, Width: photo.Width, Height: photo.Height})
	}
	if message.Document != nil {
		result.Document = &Attachment{FileID: message.Document.FileID, FileName: message.Document.FileName, MimeType: message.Document.MimeType}
	}
	if message.Video != nil {
		result.Video = &Attachment{FileID: message.Video.FileID, FileName: message.Video.FileName, MimeType: message.Video.MimeType}
	}
	if message.Audio != nil {
		result.Audio = &Attachment{FileID: message.Audio.FileID, FileName: message.Audio.FileName, MimeType: message.Audio.MimeType}
	}
	if message.Voice != nil {
		result.Voice = &Attachment{FileID: message.Voice.FileID, MimeType: message.Voice.MimeType}
	}
	if message.VideoNote != nil {
		result.VideoNote = &Attachment{FileID: message.VideoNote.FileID}
	}
	if message.Animation != nil {
		result.Animation = &Attachment{FileID: message.Animation.FileID, FileName: message.Animation.FileName, MimeType: message.Animation.MimeType}
	}
	if message.Sticker != nil {
		result.Sticker = &Attachment{FileID: message.Sticker.FileID}
	}
	return result
}

func toMarkup(keyboard Keyboard) models.ReplyMarkup {
	markup := &models.InlineKeyboardMarkup{}
	for _, row := range keyboard {
		var buttons []models.InlineKeyboardButton
		for _, button := range row {
			buttons = append(buttons, models.InlineKeyboardButton{Text: button.Text, CallbackData: button.Data})
		}
		markup.InlineKeyboard = append(markup.InlineKeyboard, buttons)
	}
	return markup
}

func emptyMarkup() models.ReplyMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{}}
}

func parseMode(options SendOptions) models.ParseMode {
	if options.Markdown {
		return models.ParseModeMarkdownV1
	}
	return ""
}

func replyParameters(options SendOptions) *models.ReplyParameters {
	if options.ReplyTo == 0 {
		return nil
	}
	return &models.ReplyParameters{MessageID: options.ReplyTo}
}

// SendMessage sends a text message.
func (a *Adapter) SendMessage(ctx context.Context, chatID int64, text string, options SendOptions) (*Message, error) {
	params := &bot.SendMessageParams{
		ChatID:          chatID,
		Text:            text,
		ParseMode:       parseMode(options),
		ReplyParameters: replyParameters(options),
	}
	if options.Keyboard != nil {
		params.ReplyMarkup = toMarkup(options.Keyboard)
	}
	sent, err := a.Bot.SendMessage(ctx, params)
	if err != nil {
		return nil, err
	}
	return ConvertMessage(sent), nil
}

// SendDocument uploads an in-memory document.
func (a *Adapter) SendDocument(ctx context.Context, chatID int64, filename string, data []byte, caption string, options SendOptions) (*Message, error) {
	params := &bot.SendDocumentParams{
		ChatID:          chatID,
		Document:        &models.InputFileUpload{Filename: filename, Data: bytes.NewReader(data)},
		Caption:         caption,
		ParseMode:       parseMode(options),
		ReplyParameters: replyParameters(options),
	}
	if options.Keyboard != nil {
		params.ReplyMarkup = toMarkup(options.Keyboard)
	}
	sent, err := a.Bot.SendDocument(ctx, params)
	if err != nil {
		return nil, err
	}
	return ConvertMessage(sent), nil
}

// EditMessageText edits a sent message.
func (a *Adapter) EditMessageText(ctx context.Context, chatID int64, messageID int, text string, options SendOptions) error {
	params := &bot.EditMessageTextParams{
		ChatID:    chatID,
		MessageID: messageID,
		Text:      text,
		ParseMode: parseMode(options),
	}
	if options.Keyboard != nil {
		params.ReplyMarkup = toMarkup(options.Keyboard)
	} else {
		params.ReplyMarkup = emptyMarkup()
	}
	_, err := a.Bot.EditMessageText(ctx, params)
	return err
}

// EditReplyMarkup removes or replaces an inline keyboard.
func (a *Adapter) EditReplyMarkup(ctx context.Context, chatID int64, messageID int, keyboard Keyboard) error {
	markup := emptyMarkup()
	if keyboard != nil {
		markup = toMarkup(keyboard)
	}
	_, err := a.Bot.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{
		ChatID:      chatID,
		MessageID:   messageID,
		ReplyMarkup: markup,
	})
	return err
}

// DeleteMessage deletes a sent message.
func (a *Adapter) DeleteMessage(ctx context.Context, chatID int64, messageID int) error {
	_, err := a.Bot.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: chatID, MessageID: messageID})
	return err
}

// SendChatAction sends a chat action such as "typing".
func (a *Adapter) SendChatAction(ctx context.Context, chatID int64, action string) error {
	_, err := a.Bot.SendChatAction(ctx, &bot.SendChatActionParams{ChatID: chatID, Action: models.ChatAction(action)})
	return err
}

// AnswerCallback answers a clicked inline button.
func (a *Adapter) AnswerCallback(ctx context.Context, callbackID, text string, showAlert bool) error {
	_, err := a.Bot.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: callbackID,
		Text:            text,
		ShowAlert:       showAlert,
	})
	return err
}

// GetFileBytes downloads a Telegram file through the configured file endpoint.
func (a *Adapter) GetFileBytes(ctx context.Context, fileID string) ([]byte, string, error) {
	file, err := a.Bot.GetFile(ctx, &bot.GetFileParams{FileID: fileID})
	if err != nil {
		return nil, "", err
	}
	url := a.FileBase + "/" + a.Token + "/" + file.FilePath
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	response, err := a.Client.Do(request)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return nil, "", fmt.Errorf("telegram file download failed: %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, "", err
	}
	return data, path.Base(file.FilePath), nil
}

// IsBadRequest reports whether the Bot API rejected a request.
func IsBadRequest(err error) bool {
	if errors.Is(err, bot.ErrorBadRequest) {
		return true
	}
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "bad request")
}

// RetryAfter returns the retry delay of a rate-limit error, if any.
func RetryAfter(err error) (time.Duration, bool) {
	var rateLimit *bot.TooManyRequestsError
	if errors.As(err, &rateLimit) {
		return time.Duration(rateLimit.RetryAfter) * time.Second, true
	}
	return 0, false
}
