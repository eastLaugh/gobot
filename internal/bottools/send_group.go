package bottools

import (
	"context"
	"fmt"
)

type sendGroupMessageKey struct{}

func WithSendGroupMessage(ctx context.Context, send func(string) error) context.Context {
	return context.WithValue(ctx, sendGroupMessageKey{}, send)
}

func SendGroupMessage(ctx context.Context, args *struct {
	Text          string `description:"要发送到当前群聊的消息正文。该正文被包装为一条 QQ 群的聊天气泡，出现在群聊中。"`
	EnsureNotSpam bool   `description:"发送前确认这条消息不是刷屏：不是重复内容，不是为了刷存在感，不是过度参与无关讨论，也不会让你在当前群聊里显得话太多。"`
}) string {
	send, ok := ctx.Value(sendGroupMessageKey{}).(func(string) error)
	if !ok {
		return "发送失败：当前上下文没有群聊发送器。"
	}
	if err := send(args.Text); err != nil {
		return fmt.Sprintf("发送失败：%v", err)
	}
	return "已发送。"
}
