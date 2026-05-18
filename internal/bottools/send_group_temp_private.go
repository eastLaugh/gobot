package bottools

import (
	"context"
	"fmt"
	"strings"
)

const groupTempPrivateDisclaimer = "\n\n我看不到你的私信，本条消息仅用于输出。"

type sendGroupTempPrivateKey struct{}

func WithSendGroupTempPrivate(ctx context.Context, send func(userID int64, message string) error) context.Context {
	return context.WithValue(ctx, sendGroupTempPrivateKey{}, send)
}

func SendGroupTempPrivate(ctx context.Context, args *struct {
	UserID int64  `description:"要发送临时私聊的群友 QQ 号（须在当前群内）。"`
	Text   string `description:"临时私聊正文（经当前群发起，无需加好友）。主人不会阅读。"`
}) string {
	send, ok := ctx.Value(sendGroupTempPrivateKey{}).(func(int64, string) error)
	if !ok {
		return "发送失败：当前上下文没有群临时私聊发送器。"
	}
	text := strings.TrimSpace(args.Text)
	if text == "" {
		return "发送失败：消息正文为空。"
	}
	if args.UserID <= 0 {
		return "发送失败：QQ 号无效。"
	}
	text += groupTempPrivateDisclaimer
	if err := send(args.UserID, text); err != nil {
		return fmt.Sprintf("发送失败：%v", err)
	}
	return "已发送群临时私聊。"
}
