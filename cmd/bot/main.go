package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"opencodebot/internal/app"
	"opencodebot/internal/config"
	"opencodebot/internal/docker"
	"opencodebot/internal/state"
	"opencodebot/internal/telegram"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.TelegramToken == "" {
		return fmt.Errorf("TELEGRAM_BOT_TOKEN environment variable is missing")
	}
	if cfg.DeepseekAPIKey == "" {
		return fmt.Errorf("DEEPSEEK_API_KEY environment variable is missing")
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return err
	}

	logger := log.New(os.Stdout, "", log.LstdFlags)
	st, err := state.Load(cfg.StateFile, cfg.Catalog)
	if err != nil {
		logger.Printf("Failed to load state: %v", err)
		st = state.New()
	}

	var application *app.App
	adapter, err := telegram.NewAdapter(cfg,
		tgbot.WithWorkers(max(4, runtime.NumCPU())),
		tgbot.WithErrorsHandler(func(err error) { logger.Printf("Telegram error: %v", err) }),
		tgbot.WithDefaultHandler(func(ctx context.Context, _ *tgbot.Bot, update *models.Update) {
			if application != nil {
				application.Handle(ctx, telegram.ConvertUpdate(update))
			}
		}),
	)
	if err != nil {
		return err
	}
	dockerAPI, err := docker.NewSDK()
	if err != nil {
		return err
	}
	application = app.New(cfg, st, adapter, dockerAPI, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Println("Bot is starting polling...")
	adapter.Bot.Start(ctx)
	return nil
}
