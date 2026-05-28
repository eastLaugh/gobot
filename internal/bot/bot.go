package bot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/eastlaugh/gobot/internal/agent"
	"github.com/eastlaugh/gobot/internal/config"
	"github.com/eastlaugh/gobot/internal/session"
)

const configPath = "go.toml"

const (
	cmdPing = "/ping"
	cmdHang = "/hang"
	cmdBill = "/bill"

	sessionPauseSummaryEvent = "【系统事件】本 session 即将结束。请简要总结本轮对话要点；如有值得跨 session 保存的信息，可调用记忆工具更新记忆。除非非常必要，不要发送群消息。"
)

type Bot struct {
	systemPrompt  string
	exampleConfig []byte
	configPath    string

	wsToken string
	cfg     *config.Config
	groups  map[int64]*session.Group
	agent   *agent.Agent
}

func New(systemPrompt string, exampleConfig []byte) (*Bot, error) {
	if err := config.LoadDotEnv(".env"); err != nil {
		return nil, fmt.Errorf("load .env: %w", err)
	}
	wsToken := os.Getenv("ONEBOT_WS_TOKEN")
	openAIAPIKey := os.Getenv("OPENAI_API_KEY")
	openAIBaseURL := os.Getenv("OPENAI_BASE_URL")
	openAIModel := os.Getenv("OPENAI_MODEL")
	steamAPIKey := os.Getenv("STEAM_API_KEY")
	if wsToken == "" || openAIAPIKey == "" || openAIBaseURL == "" || openAIModel == "" || steamAPIKey == "" {
		return nil, errors.New("ONEBOT_WS_TOKEN / OPENAI_API_KEY / OPENAI_BASE_URL / OPENAI_MODEL / STEAM_API_KEY is required")
	}
	if err := config.Ensure(configPath, exampleConfig); err != nil {
		return nil, fmt.Errorf("ensure go.toml: %w", err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("load go.toml: %w", err)
	}
	groups := session.NewGroups(cfg)
	return &Bot{
		systemPrompt:  systemPrompt,
		exampleConfig: exampleConfig,
		configPath:    configPath,
		wsToken:       wsToken,
		cfg:           cfg,
		groups:        groups,
		agent:         agent.New(openAIBaseURL, openAIAPIKey, openAIModel, systemPrompt),
	}, nil
}

func (b *Bot) Run(ctx context.Context) error {
	unlock, err := acquireInstanceLock()
	if err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	defer unlock()

	log.Printf("target groups=%v maintainers=%v", groupIDs(b.groups), b.cfg.Maintainers)
	log.Printf("reverse ws url=%s", b.cfg.OneBot.ReverseWSURL)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sig)

	err = b.runWSLoop(ctx, sig)
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func groupIDs(groups map[int64]*session.Group) []int64 {
	ids := make([]int64, 0, len(groups))
	for id := range groups {
		ids = append(ids, id)
	}
	return ids
}
