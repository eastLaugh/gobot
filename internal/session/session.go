package session

import (
	"fmt"
	"strings"
	"time"

	"github.com/eastlaugh/gobot/internal/bottools"
	"github.com/eastlaugh/gobot/internal/onebot"
	"github.com/openai/openai-go/v3"
)

type Session struct {
	LastAt          time.Time
	Memory          string
	Messages        []openai.ChatCompletionMessageParamUnion
	PendingUserText strings.Builder
	CostYuan        float64
}

func EventTime(ev onebot.Event) time.Time {
	t := time.Unix(ev.Time, 0)
	if ev.Time == 0 {
		t = time.Now()
	}
	return t
}

func (s *Session) ExpiredAt(t time.Time) bool {
	return !s.LastAt.IsZero() && t.Sub(s.LastAt) > 20*time.Minute
}

func (s *Session) Reset(memoryFile string) {
	s.Memory = bottools.MemoryPromptSection(memoryFile)
	s.Messages = nil
	s.PendingUserText.Reset()
	s.LastAt = time.Time{}
	s.CostYuan = 0
}

func (s *Session) ClearContext(memoryFile string) {
	s.Memory = bottools.MemoryPromptSection(memoryFile)
	s.Messages = nil
	s.CostYuan = 0
}

func (s *Session) HasContent() bool {
	return !s.LastAt.IsZero() || len(s.Messages) > 0 || s.PendingUserText.Len() > 0
}

func (s *Session) Append(ev onebot.Event, text, memoryFile string) {
	t := EventTime(ev)
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

func AppendObserveReminder(messages []openai.ChatCompletionMessageParamUnion) []openai.ChatCompletionMessageParamUnion {
	return append(messages, openai.UserMessage(observeToolReminder))
}

func (s *Session) Snapshot() (string, []openai.ChatCompletionMessageParamUnion) {
	messages := append([]openai.ChatCompletionMessageParamUnion(nil), s.Messages...)
	if s.PendingUserText.Len() > 0 {
		messages = append(messages, openai.UserMessage(s.PendingUserText.String()))
	}
	return s.Memory, messages
}

func (s *Session) AppendMessages(messages []openai.ChatCompletionMessageParamUnion) {
	if s.PendingUserText.Len() > 0 {
		s.Messages = append(s.Messages, openai.UserMessage(s.PendingUserText.String()))
		s.PendingUserText.Reset()
	}
	s.Messages = append(s.Messages, messages...)
}
