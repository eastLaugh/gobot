package bottools

import (
	"context"
	_ "embed"
	"encoding/base64"
	"fmt"
)

//go:embed stickers/xiaolan.png
var xiaolanImage []byte

//go:embed stickers/niyijiku.png
var niyijikuImage []byte

//go:embed stickers/buyao.png
var buyaoImage []byte

func sendEmbeddedImage(ctx context.Context, image []byte, label string) string {
	send, ok := ctx.Value(sendGroupMessageKey{}).(func(string) error)
	if !ok {
		return "发送失败：当前上下文没有群聊发送器。"
	}
	msg := "[CQ:image,file=base64://" + base64.StdEncoding.EncodeToString(image) + "]"
	if err := send(msg); err != nil {
		return fmt.Sprintf("发送失败：%v", err)
	}
	return "已发送「" + label + "」梗图。"
}

func SendLaughBroken(ctx context.Context, _ *struct{}) string {
	return sendEmbeddedImage(ctx, xiaolanImage, "笑烂了")
}

func SendNiYiJiKu(ctx context.Context, _ *struct{}) string {
	return sendEmbeddedImage(ctx, niyijikuImage, "你已急哭")
}

func SendBuYao(ctx context.Context, _ *struct{}) string {
	return sendEmbeddedImage(ctx, buyaoImage, "不要")
}
