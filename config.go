package main

import (
	"fmt"

	"github.com/BurntSushi/toml"
)

type botConfig struct {
	OneBot oneBotConfig  `toml:"onebot"`
	OpenAI openAIConfig  `toml:"openai"`
	Groups []groupConfig `toml:"groups"`
}

type oneBotConfig struct {
	ReverseWSURL string `toml:"reverse_ws_url"`
}

type openAIConfig struct {
	BaseURL string `toml:"base_url"`
	Model   string `toml:"model"`
}

type groupConfig struct {
	QQ         int64  `toml:"qq"`
	Prompt     string `toml:"prompt"`
	MemoryFile string `toml:"memory_file"`
}

type groupState struct {
	ID         int64
	Prompt     string
	MemoryFile string
	Runtime    groupRuntime
}

func loadBotConfig(path string) (*botConfig, error) {
	var cfg botConfig
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, err
	}
	if cfg.OneBot.ReverseWSURL == "" {
		return nil, fmt.Errorf("onebot.reverse_ws_url is required")
	}
	if cfg.OpenAI.BaseURL == "" {
		return nil, fmt.Errorf("openai.base_url is required")
	}
	if cfg.OpenAI.Model == "" {
		return nil, fmt.Errorf("openai.model is required")
	}
	if len(cfg.Groups) == 0 {
		return nil, fmt.Errorf("groups is required")
	}
	for i, group := range cfg.Groups {
		if group.QQ == 0 {
			return nil, fmt.Errorf("groups[%d].qq is required", i)
		}
		if group.Prompt == "" {
			return nil, fmt.Errorf("groups[%d].prompt is required", i)
		}
		if group.MemoryFile == "" {
			return nil, fmt.Errorf("groups[%d].memory_file is required", i)
		}
	}
	return &cfg, nil
}

func loadGroupStates(cfg *botConfig) (map[int64]*groupState, error) {
	groups := make(map[int64]*groupState, len(cfg.Groups))
	for _, group := range cfg.Groups {
		groups[group.QQ] = &groupState{
			ID:         group.QQ,
			Prompt:     group.Prompt,
			MemoryFile: group.MemoryFile,
		}
	}
	return groups, nil
}
