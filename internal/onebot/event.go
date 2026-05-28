package onebot

import (
	"encoding/json"
	"fmt"
	"strings"
)

type Event struct {
	Time        int64           `json:"time"`
	SelfID      int64           `json:"self_id"`
	PostType    string          `json:"post_type"`
	MessageType string          `json:"message_type"`
	SubType     string          `json:"sub_type"`
	GroupID     int64           `json:"group_id"`
	UserID      int64           `json:"user_id"`
	Sender      Sender          `json:"sender"`
	RawMessage  string          `json:"raw_message"`
	Message     json.RawMessage `json:"message"`
}

type Sender struct {
	Nickname string `json:"nickname"`
	Card     string `json:"card"`
}

func ExtractText(ev Event) string {
	if ev.RawMessage != "" {
		return ev.RawMessage
	}
	var s string
	if err := json.Unmarshal(ev.Message, &s); err == nil {
		return s
	}
	type seg struct {
		Type string            `json:"type"`
		Data map[string]string `json:"data"`
	}
	var segs []seg
	if err := json.Unmarshal(ev.Message, &segs); err == nil {
		var b strings.Builder
		for _, sg := range segs {
			if sg.Type == "text" {
				b.WriteString(sg.Data["text"])
			}
			if sg.Type == "at" {
				fmt.Fprintf(&b, "[CQ:at,qq=%s]", sg.Data["qq"])
			}
		}
		return b.String()
	}
	return ""
}
