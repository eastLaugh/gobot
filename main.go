package main

import (
	_ "embed"

	"github.com/eastlaugh/gobot/internal/app"
)

//go:embed system.prompt
var systemPrompt string

//go:embed go.toml.example
var exampleConfig []byte

func main() {
	app.Run(systemPrompt, exampleConfig)
}
