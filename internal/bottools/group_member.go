package bottools

import (
	"context"
	"fmt"
)

type groupMembersKey struct{}
type groupPokeKey struct{}

func WithGroupMembers(ctx context.Context, query func(context.Context, string, int) (string, error)) context.Context {
	return context.WithValue(ctx, groupMembersKey{}, query)
}

func WithGroupPoke(ctx context.Context, poke func(int64) error) context.Context {
	return context.WithValue(ctx, groupPokeKey{}, poke)
}

func QueryGroupMembers(ctx context.Context, args *struct {
	Query string `description:"可选。按 QQ 号、昵称、群名片子串过滤当前 QQ 群成员；留空则列出一部分群成员。"`
	Limit int    `description:"最多返回多少个成员，避免一次塞太多群员进上下文。"`
}) string {
	query, ok := ctx.Value(groupMembersKey{}).(func(context.Context, string, int) (string, error))
	if !ok {
		return "查询失败：当前上下文没有群成员查询器。"
	}
	out, err := query(ctx, args.Query, args.Limit)
	if err != nil {
		return fmt.Sprintf("查询失败：%v", err)
	}
	return out
}

func PokeGroupMember(ctx context.Context, args *struct {
	QQ     int64  `description:"要在当前 QQ 群里戳一戳的群成员 QQ 号。"`
	Reason string `description:"可选。你为什么要戳这个人，只作为内部判断说明，不会发到群里。"`
}) string {
	poke, ok := ctx.Value(groupPokeKey{}).(func(int64) error)
	if !ok {
		return "戳一戳失败：当前上下文没有群戳一戳发送器。"
	}
	if args.QQ == 0 {
		return "戳一戳失败：QQ 为空。"
	}
	if err := poke(args.QQ); err != nil {
		return fmt.Sprintf("戳一戳失败：%v", err)
	}
	return "已戳一戳。"
}
