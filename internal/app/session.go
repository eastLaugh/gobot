package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/eastlaugh/gobot/internal/bottools"
	"github.com/openai/openai-go/v3"
)

type OneBotEvent struct {
	Time        int64           `json:"time"`
	SelfID      int64           `json:"self_id"`
	PostType    string          `json:"post_type"`
	MessageType string          `json:"message_type"`
	SubType     string          `json:"sub_type"`
	GroupID     int64           `json:"group_id"`
	UserID      int64           `json:"user_id"`
	Sender      OneBotSender    `json:"sender"`
	RawMessage  string          `json:"raw_message"`
	Message     json.RawMessage `json:"message"`
}

type OneBotSender struct {
	Nickname string `json:"nickname"`
	Card     string `json:"card"`
}

type chatSession struct {
	LastAt          time.Time
	Memory          string
	Messages        []openai.ChatCompletionMessageParamUnion
	PendingUserText strings.Builder
	CostYuan        float64
}

type groupRuntime struct {
	Session            chatSession
	Timer              *time.Timer
	TimerSeq           int64
	Observing          bool
	Dirty              bool
	Pending            int
	PendingAtBot       bool
	PendingLastAt      time.Time
	ObserveCancel      context.CancelFunc
	Generation         int64
	PausePending bool
	PauseRunning bool
}

func eventTime(ev OneBotEvent) time.Time {
	t := time.Unix(ev.Time, 0)
	if ev.Time == 0 {
		t = time.Now()
	}
	return t
}

func (s *chatSession) ExpiredAt(t time.Time) bool {
	return !s.LastAt.IsZero() && t.Sub(s.LastAt) > 20*time.Minute
}

func (s *chatSession) Reset(memoryFile string) {
	s.Memory = bottools.MemoryPromptSection(memoryFile)
	s.Messages = nil
	s.PendingUserText.Reset()
	s.LastAt = time.Time{}
	s.CostYuan = 0
}

// ClearContext 结束 session 长上下文，保留尚未 observe 的 pending 消息。
func (s *chatSession) ClearContext(memoryFile string) {
	s.Memory = bottools.MemoryPromptSection(memoryFile)
	s.Messages = nil
	s.CostYuan = 0
}

func (s *chatSession) HasContent() bool {
	return !s.LastAt.IsZero() || len(s.Messages) > 0 || s.PendingUserText.Len() > 0
}

func (s *chatSession) Append(ev OneBotEvent, text string, memoryFile string) {
	t := eventTime(ev)
	if s.LastAt.IsZero() {
		s.Reset(memoryFile)
	}
	name := ev.Sender.Card
	if name == "" {
		name = ev.Sender.Nickname
	}
	s.LastAt = t
	var b strings.Builder
	fmt.Fprintf(&b, "[%s][user=%d", t.Format("2006-01-02 15:04:05"), ev.UserID)
	if name != "" {
		fmt.Fprintf(&b, "][name=%s", name)
	}
	fmt.Fprintf(&b, "] %s", text)
	if s.PendingUserText.Len() > 0 {
		s.PendingUserText.WriteString("\n")
	}
	s.PendingUserText.WriteString(b.String())
}

const observeToolReminder = `【提醒】要参与请调工具；只写 content 群友看不到。`

func appendObserveReminder(messages []openai.ChatCompletionMessageParamUnion) []openai.ChatCompletionMessageParamUnion {
	return append(messages, openai.UserMessage(observeToolReminder))
}

func (s *chatSession) Snapshot() (string, []openai.ChatCompletionMessageParamUnion) {
	messages := append([]openai.ChatCompletionMessageParamUnion(nil), s.Messages...)
	if s.PendingUserText.Len() > 0 {
		messages = append(messages, openai.UserMessage(s.PendingUserText.String()))
	}
	return s.Memory, messages
}

func (s *chatSession) AppendMessages(messages []openai.ChatCompletionMessageParamUnion) {
	if s.PendingUserText.Len() > 0 {
		s.Messages = append(s.Messages, openai.UserMessage(s.PendingUserText.String()))
		s.PendingUserText.Reset()
	}
	s.Messages = append(s.Messages, messages...)
}

func coldDelay(pending int, atBot bool) time.Duration {
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
