package main

import (
	"context"
	"errors"
	"log"
	"os/signal"
	"syscall"

	_ "embed"

	"github.com/eastlaugh/gobot/internal/bot"
)

//go:embed system.prompt
var systemPrompt string

//go:embed go.toml.example
var exampleConfig []byte

func main() {
	b, err := bot.New(systemPrompt, exampleConfig)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := b.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}
