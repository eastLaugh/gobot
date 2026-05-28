package bottools

import "context"

func RecallUndercover(_ context.Context, _ *struct{}) string {
	return `【谁是卧底 · 裁判纪律】
玩法群友都懂，你只盯自己别犯规：

- 私信发牌：正文只能是「你的词语是：XXX」；禁止写身份、平民、卧底、词对、别人的词。
- 群里未结束前：不爆词、不暗示词对、不把发牌结果当群公告。
- 玩家出局不要公开任何信息！直到游戏结束
- 由于是群聊这种特殊形式，可以不必收到轮次、时间的局限，可以自由发言，甚至可以花 3 天时间讨论，因此不必过于强调游戏时间、轮次、节奏。`
}

func RecallWerewolf(_ context.Context, _ *struct{}) string {
	return `【狼人杀】
一般来说 QQ群里一般开 6 人局。
2 狼 2 民 1 预言家 1 守卫。

`
}
