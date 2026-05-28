package app

import (
	"strconv"
	"strings"
)

// IncomingGroupMessage 在 OneBot 原始事件进入 session / 冷场逻辑前的预处理结果。
type IncomingGroupMessage struct {
	Event OneBotEvent
	Text  string
	AtBot bool
}

type IncomingHandler func(IncomingGroupMessage) IncomingGroupMessage

func RunIncomingHandlers(m IncomingGroupMessage, handlers ...IncomingHandler) IncomingGroupMessage {
	for _, h := range handlers {
		m = h(m)
	}
	return m
}

func AtBotHandler(botQQ int64) IncomingHandler {
	prefix := "[CQ:at,qq=" + strconv.FormatInt(botQQ, 10) + "]"
	return func(m IncomingGroupMessage) IncomingGroupMessage {
		m.AtBot = strings.Contains(m.Text, prefix)
		return m
	}
}
