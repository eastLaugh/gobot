package incoming

import (
	"strconv"
	"strings"

	"github.com/eastlaugh/gobot/internal/onebot"
)

type GroupMessage struct {
	Event onebot.Event
	Text  string
	AtBot bool
}

type Handler func(GroupMessage) GroupMessage

func Run(m GroupMessage, handlers ...Handler) GroupMessage {
	for _, h := range handlers {
		m = h(m)
	}
	return m
}

func AtBot(botQQ int64) Handler {
	prefix := "[CQ:at,qq=" + strconv.FormatInt(botQQ, 10) + "]"
	return func(m GroupMessage) GroupMessage {
		m.AtBot = strings.Contains(m.Text, prefix)
		return m
	}
}
