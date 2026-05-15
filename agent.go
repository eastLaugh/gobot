package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"text/template"

	"github.com/eastLaugh/web-app-go/go/pkg/tools"
	"github.com/eastlaugh/gobot/internal/bottools"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

const maxToolRounds = 10

type chatAgent struct {
	client   *openai.Client
	model    string
	registry *tools.Registry
}

func newChatAgent(openAIBaseURL, openAIAPIKey, openAIModel string) *chatAgent {
	client := openai.NewClient(
		option.WithBaseURL(openAIBaseURL),
		option.WithAPIKey(openAIAPIKey),
	)
	registry := tools.New(
		bottools.QueryFinalsPlayer, "查询 The Finals 当前 S10 跨平台排行榜中的玩家排名、段位、分数。只覆盖榜单前 10000 名。",
		bottools.QuerySteamStatus, "查询 Steam 玩家在线状态和正在玩的游戏。需要 SteamID、主页链接或自定义主页名，且对方资料公开。",
		bottools.ReplaceMemoryPrompt, "替换当前群的整份跨 session 记忆 prompt。用于长期记忆的增删改、压缩、去重、融合更新。这个工具不会自动发群消息，且新记忆从下一个 session 开始注入。",
		bottools.QueryGroupMembers, "查询当前 QQ 群成员列表，可按 QQ 号、昵称、群名片过滤。适合需要知道群里有哪些人、某个 QQ 对应谁、或 @ 某人前确认 QQ 号。",
		bottools.PokeGroupMember, "在当前 QQ 群里戳一戳某个群成员。适合轻微互动、提醒、开玩笑；不要频繁调用。",
		bottools.WebSearch, "搜索网页，返回标题、链接和摘要。结果可能有噪声，适合不知道具体网址时先找线索。",
		tools.Fetch_web_page, "抓取指定 URL 的网页正文。适合读取已知网页或搜索结果中的页面。",
		bottools.SendGroupMessage, "向当前 QQ 群发送一条群友可见的消息。调用前要确认不是刷屏，并在参数里显式声明。一条群消息由一个气泡承载，大概 6 个气泡就占用一个屏幕。因此不宜连续调用超过 3次。",
	)
	tools.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return &chatAgent{client: &client, model: openAIModel, registry: registry}
}

const systemPrompt = `QQ 是即时通讯软件，发言要短、自然、中文、有人味。不要解释推理或工具过程。不要仅仅为了意义而说话。不要重复已经说过的话。
你正在 QQ 群聊里观察消息，群里的人有时会互相展开讨论，有些讨论与你无关，有些讨论与你有关。他们会使用 CQ 代码来表示一些特别操作，比如 @ 别人。
接下来，你将会收到 QQ 群 session，你收到的消息被称之为一个 session，在 session 中会忠实记录群友的聊天记录。你需要根据 session 判断是否要调用工具。
你的普通输出 content 是内部记录，不会自动出现在 QQ 群里，不会被任何人知道，只会被追加进当前 session，供未来的你参考。content 里只写简短判断，比如“这条和我无关，沉默”或“需要查资料后回复”。不要在 content 里写括号旁白、动作描写、卖萌台词、舞台表演。

群消息格式类似：
[2026-05-13 19:18:05][user=数字][name=某人] ...

[user=数字] 是群友 QQ 号。

你发群消息时，可以用 [CQ:at,qq=对方QQ] @ 别人。
如果消息里 @ 的不是你的 QQ，说明他在 @ 别人，而不是你；除非消息和你有关，否则不要代替别人回答。

如果你想要参与讨论，请调用发群消息工具。发群消息工具的 Text 参数才是 QQ 群里真实出现的消息气泡；只有这里应该写给群友看的话。普通输出 content、工具调用本身、工具结果都不会让群友看到。
如果你被 @、被点名、或正在直接回答群友问题，只要你决定让群友看到任何文字，就必须调用发群消息工具。assistant content 只能写内部判断，禁止写完整回复，禁止写 CQ at，禁止写任何希望群友看到的内容。把回复写进 content 等于没有回复。

超过 1 小时没有任何新消息会开启新 session。你将不再看到之前的 session 消息，但是你拥有跨越 session 的记忆工具。
你能看到群友消息、自己的普通输出 content、工具调用和工具结果。它们会插入只读的 chat messages prefix，以便于你未来做出更多操作。不要把以前 content 里的内部记录当成群消息风格，也不要把工具结果当成已经发给群友的话。

像真实人类群友一样参与：你大多数时候旁观；被 @、被点名、被直接问到时积极处理；没人叫你且只是群友互聊时，可以选择沉默。
不要刷屏，确保群友发的消息和你的发消息比例大约为 3:1，可视情况调整。每次调用发送群消息工具前，请先反思自己是否可能刷屏。
QQ 群单条消息由气泡承载，不宜过长，过于严肃。单个气泡的屏占比较高，因此不要刷屏。
积极参与有趣的讨论。有时可以发些莫名其妙的东西，人类就是这种生物。
你可以连续调用多个工具，也可以把一次自然发言分成多条群消息；完成当前意图后就停止。
群友明确让你记住某件事，或者你判断某些事实值得跨 session 保存时，可以调用记忆工具重写整份长期记忆 prompt。记忆工具是整份替换，不是追加；你应该主动压缩重复内容、删除过时内容、融合新旧事实，让未来的你读起来像一份清晰背景，而不是流水账。通常再发一条很短的确认。`

func buildSystemPrompt(promptTemplate, memory string) (string, error) {
	t, err := template.New("prompt").Option("missingkey=error").Parse(promptTemplate)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	err = t.Execute(&b, struct {
		SystemPrompt string
		MemoryPrompt string
	}{
		SystemPrompt: systemPrompt,
		MemoryPrompt: memory,
	})
	return b.String(), err
}

func (a *chatAgent) Observe(ctx context.Context, groupID int64, prompt, memory string, sessionMessages []openai.ChatCompletionMessageParamUnion) ([]openai.ChatCompletionMessageParamUnion, error) {
	system, err := buildSystemPrompt(prompt, memory)
	if err != nil {
		return nil, err
	}
	messages := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(system),
	}
	messages = append(messages, sessionMessages...)
	var added []openai.ChatCompletionMessageParamUnion

	for round := 0; round < maxToolRounds; round++ {
		writeRunRequest(groupID, a.model, round, messages, a.registry.ToParams())
		resp, err := a.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
			Model:    a.model,
			Messages: messages,
			Tools:    a.registry.ToParams(),
		})
		if err != nil {
			return added, err
		}
		if len(resp.Choices) == 0 {
			return added, errors.New("empty choices")
		}
		msg := resp.Choices[0].Message
		messages = append(messages, msg.ToParam())
		added = append(added, msg.ToParam())

		if len(msg.ToolCalls) == 0 {
			if msg.Content != "" {
				log.Printf("模型未调用工具，普通输出已忽略：%q", msg.Content)
			} else {
				log.Printf("模型未调用工具，也没有普通输出")
			}
			return added, nil
		}

		for _, tc := range msg.ToolCalls {
			if tc.Type != "function" {
				continue
			}
			out, err := a.registry.Execute(ctx, tc.Function.Name, tc.Function.Arguments)
			if err != nil {
				out = err.Error()
			}
			log.Printf("工具调用：%s 参数=%s 结果=%s", tc.Function.Name, tc.Function.Arguments, truncateForLog(out, 200))
			toolMessage := openai.ToolMessage(out, tc.ID)
			messages = append(messages, toolMessage)
			added = append(added, toolMessage)
		}
	}
	return added, fmt.Errorf("超过 %d 轮工具调用上限", maxToolRounds)
}

func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func writeRunRequest(groupID int64, model string, round int, messages []openai.ChatCompletionMessageParamUnion, tools []openai.ChatCompletionToolUnionParam) {
	if err := os.MkdirAll(".run", 0700); err != nil {
		log.Printf("写入调试请求失败：%v", err)
		return
	}
	b, err := json.MarshalIndent(map[string]any{
		"model":    model,
		"round":    round,
		"messages": messages,
		"tools":    tools,
	}, "", "  ")
	if err != nil {
		log.Printf("序列化调试请求失败：%v", err)
		return
	}
	if err := os.WriteFile(filepath.Join(".run", fmt.Sprintf("%d.latest.json", groupID)), b, 0600); err != nil {
		log.Printf("写入调试请求失败：%v", err)
	}
}
