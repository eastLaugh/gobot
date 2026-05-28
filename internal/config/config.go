package config

import (
	"fmt"

	"github.com/BurntSushi/toml"
)

type Config struct {
	OneBot      OneBot  `toml:"onebot"`
	Maintainers []int64 `toml:"maintainers"`
	Groups      []Group `toml:"groups"`
}

type OneBot struct {
	ReverseWSURL string `toml:"reverse_ws_url"`
}

type Group struct {
	QQ         int64  `toml:"qq"`
	Prompt     string `toml:"prompt"`
	MemoryFile string `toml:"memory_file"`
}

func Load(path string) (*Config, error) {
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, err
	}
	if cfg.OneBot.ReverseWSURL == "" {
		return nil, fmt.Errorf("onebot.reverse_ws_url is required")
	}
	if len(cfg.Maintainers) == 0 {
		return nil, fmt.Errorf("maintainers is required")
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

func (c *Config) IsMaintainer(qq int64) bool {
	for _, m := range c.Maintainers {
		if m == qq {
			return true
		}
	}
	return false
}
