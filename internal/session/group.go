package session

import (
	"context"
	"time"

	"github.com/eastlaugh/gobot/internal/config"
)

const CostLimitYuan = 3.0

type Group struct {
	ID         int64
	Prompt     string
	MemoryFile string
	Runtime    Runtime
}

type Runtime struct {
	Session       Session
	Timer         *time.Timer
	TimerSeq      int64
	Observing     bool
	Dirty         bool
	Pending       int
	PendingAtBot  bool
	PendingLastAt time.Time
	ObserveCancel context.CancelFunc
	Generation    int64
	PausePending  bool
	PauseRunning  bool
}

func NewGroups(cfg *config.Config) map[int64]*Group {
	groups := make(map[int64]*Group, len(cfg.Groups))
	for _, g := range cfg.Groups {
		groups[g.QQ] = &Group{
			ID:         g.QQ,
			Prompt:     g.Prompt,
			MemoryFile: g.MemoryFile,
		}
	}
	return groups
}

func ColdDelay(pending int, atBot bool) time.Duration {
	if atBot {
		return 0
	}
	if pending == 1 {
		return 6 * time.Second
	}
	if pending == 2 {
		return 15 * time.Second
	}
	return 30 * time.Second
}
