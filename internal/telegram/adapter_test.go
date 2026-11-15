package telegram

import (
	"testing"

	"github.com/go-telegram/bot/models"
)

func TestNormalizeAPIBase(t *testing.T) {
	cases := map[string]string{
		"http://host:8081/bot":     "http://host:8081",
		"http://host:8081/bot/":    "http://host:8081",
		"https://api.telegram.org": "https://api.telegram.org",
		"":                         "",
	}
	for input, want := range cases {
		if got := NormalizeAPIBase(input); got != want {
			t.Fatalf("NormalizeAPIBase(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestConvertUpdateMessage(t *testing.T) {
	update := &models.Update{Message: &models.Message{
		ID:       9,
		Chat:     models.Chat{ID: 5},
		From:     &models.User{ID: 7},
		Text:     "hi",
		Caption:  "cap",
		Photo:    []models.PhotoSize{{FileID: "p", Width: 10, Height: 20}},
		Document: &models.Document{FileID: "d", FileName: "a.txt", MimeType: "text/plain"},
	}}
	converted := ConvertUpdate(update)
	if converted.ChatID != 5 || converted.UserID != 7 || converted.Message.ID != 9 || converted.Message.Text != "hi" {
		t.Fatalf("converted = %+v", converted)
	}
	if len(converted.Message.Photo) != 1 || converted.Message.Photo[0].FileID != "p" {
		t.Fatalf("photo = %+v", converted.Message.Photo)
	}
	if converted.Message.Document == nil || converted.Message.Document.FileName != "a.txt" {
		t.Fatalf("document = %+v", converted.Message.Document)
	}
}

func TestConvertUpdateCallback(t *testing.T) {
	update := &models.Update{CallbackQuery: &models.CallbackQuery{
		ID:   "cb",
		From: models.User{ID: 3},
		Data: "modelmenu:x",
		Message: models.MaybeInaccessibleMessage{
			Type:    models.MaybeInaccessibleMessageTypeMessage,
			Message: &models.Message{ID: 42, Chat: models.Chat{ID: 8}},
		},
	}}
	converted := ConvertUpdate(update)
	if converted.ChatID != 8 || converted.UserID != 3 || converted.Callback == nil || converted.Callback.Data != "modelmenu:x" || converted.Callback.Message.ID != 42 {
		t.Fatalf("converted = %+v", converted)
	}
}
