package agent

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

type Agent struct {
	client       *openai.Client
	model        string
	systemPrompt string
	registry     *tools.Registry
}

func New(openAIBaseURL, openAIAPIKey, openAIModel, systemPrompt string) *Agent {
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
		bottools.SendGroupTempPrivate, "向当前群内某位群友发送群临时私聊（经本群发起，无需加好友）。仅用于单独输出信息；主人不会阅读。不要代替群聊回复。",
		// 账单仅维护者 @bot /bill，走 bottools.QueryGroupBilling，不注册给模型。
		// bottools.QueryGroupBilling, "查询 QQ 群 token 账单。默认查当前群累计费用；可设 AllGroups 查看所有群的累计花费。数据来自持久化的按群计费记录。",
		bottools.RecallUndercover, "玩谁是卧底前请先回忆。返回裁判纪律（纯文本，不发群）。",
		bottools.RecallWerewolf, "玩狼人杀前请先回忆。返回上帝纪律（纯文本，不发群）。",
		bottools.SendLaughBroken, "向当前群发送「笑烂了」梗图（内置图片，一条群消息）。",
		bottools.SendNiYiJiKu, "向当前群发送「你已急哭」梗图（内置图片，一条群消息）。",
		bottools.SendBuYao, "向当前群发送「不要」卖萌梗图（内置图片，一条群消息）。",
	)
	tools.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return &Agent{client: &client, model: openAIModel, systemPrompt: systemPrompt, registry: registry}
}

type promptData struct {
	SystemPrompt string
	BotQQ        int64
	MemoryPrompt string
}

func renderPrompt(name, text string, data promptData) (string, error) {
	t, err := template.New(name).Option("missingkey=error").Parse(text)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	err = t.Execute(&b, data)
	return b.String(), err
}

func buildSystemPrompt(promptTemplate, systemPrompt string, botQQ int64, memory string) (string, error) {
	data := promptData{BotQQ: botQQ, MemoryPrompt: memory}
	renderedSystem, err := renderPrompt("system", systemPrompt, data)
	if err != nil {
		return "", err
	}
	data.SystemPrompt = renderedSystem
	return renderPrompt("group", promptTemplate, data)
}

func (a *Agent) Observe(ctx context.Context, groupID, botQQ int64, prompt, memory string, sessionMessages []openai.ChatCompletionMessageParamUnion) ([]openai.ChatCompletionMessageParamUnion, error) {
	system, err := buildSystemPrompt(prompt, a.systemPrompt, botQQ, memory)
	if err != nil {
		return nil, err
	}
	messages := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(system),
	}
	messages = append(messages, sessionMessages...)
	var added []openai.ChatCompletionMessageParamUnion
	if acc, ok := ctx.Value(observeUsageKey{}).(*ObserveUsageAcc); ok && acc != nil && acc.GroupID == 0 {
		acc.GroupID = groupID
	}

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
			reportTokenUsage(ctx, TokenUsage{
				GroupID:          groupID,
				Round:            round,
				Model:            a.model,
				PromptTokens:     resp.Usage.PromptTokens,
				CachedTokens:     resp.Usage.PromptTokensDetails.CachedTokens,
				CompletionTokens: resp.Usage.CompletionTokens,
				TotalTokens:      resp.Usage.TotalTokens,
			})
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
			log.Printf("工具调用：%s 参数=%s 结果=%s", tc.Function.Name, tc.Function.Arguments, TruncateForLog(out, 200))
			toolMessage := openai.ToolMessage(out, tc.ID)
			messages = append(messages, toolMessage)
			added = append(added, toolMessage)
		}
		reportTokenUsage(ctx, TokenUsage{
			GroupID:          groupID,
			Round:            round,
			Model:            a.model,
			PromptTokens:     resp.Usage.PromptTokens,
			CachedTokens:     resp.Usage.PromptTokensDetails.CachedTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		})
	}
	return added, fmt.Errorf("超过 %d 轮工具调用上限", maxToolRounds)
}

func TruncateForLog(s string, n int) string {
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
