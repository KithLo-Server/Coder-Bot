// Package telegram defines a small, fakeable Telegram interface and its adapter.
package telegram

import "context"

// Button is one inline keyboard callback button.
type Button struct {
	Text string
	Data string
}

// Keyboard is a grid of inline buttons.
type Keyboard [][]Button

// PhotoSize is one resolution of an uploaded photo.
type PhotoSize struct {
	FileID string
	Width  int
	Height int
}

// Attachment is a file-backed document or media attachment.
type Attachment struct {
	FileID   string
	FileName string
	MimeType string
}

// Message is the subset of Telegram messages the bot uses.
type Message struct {
	ID        int
	ChatID    int64
	Text      string
	Caption   string
	Photo     []PhotoSize
	Document  *Attachment
	Video     *Attachment
	Audio     *Attachment
	Voice     *Attachment
	VideoNote *Attachment
	Animation *Attachment
	Sticker   *Attachment
}

// Callback is a clicked inline button.
type Callback struct {
	ID      string
	Data    string
	Message *Message
}

// Update is one Telegram update.
type Update struct {
	ChatID   int64
	UserID   int64
	Message  *Message
	Callback *Callback
}

// SendOptions controls message delivery.
type SendOptions struct {
	Markdown bool
	Keyboard Keyboard
	ReplyTo  int
}

// Messenger is the Telegram surface used by handlers and tests.
type Messenger interface {
	SendMessage(ctx context.Context, chatID int64, text string, options SendOptions) (*Message, error)
	SendDocument(ctx context.Context, chatID int64, filename string, data []byte, caption string, options SendOptions) (*Message, error)
	EditMessageText(ctx context.Context, chatID int64, messageID int, text string, options SendOptions) error
	EditReplyMarkup(ctx context.Context, chatID int64, messageID int, keyboard Keyboard) error
	DeleteMessage(ctx context.Context, chatID int64, messageID int) error
	SendChatAction(ctx context.Context, chatID int64, action string) error
	AnswerCallback(ctx context.Context, callbackID, text string, showAlert bool) error
	GetFileBytes(ctx context.Context, fileID string) ([]byte, string, error)
}
