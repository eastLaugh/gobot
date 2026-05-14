package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	webtools "github.com/eastLaugh/web-app-go/go/pkg/tools"
	"github.com/gorilla/websocket"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

func loadDotEnv(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.Index(line, "=")
		if i <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:i])
		val := strings.TrimSpace(line[i+1:])
		val = strings.Trim(val, `"'`)
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		_ = os.Setenv(key, val)
	}
	return nil
}

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("%s is required", key)
	}
	return v
}

type OneBotEvent struct {
	Time        int64           `json:"time"`
	SelfID      int64           `json:"self_id"`
	PostType    string          `json:"post_type"`
	MessageType string          `json:"message_type"`
	SubType     string          `json:"sub_type"`
	GroupID     int64           `json:"group_id"`
	UserID      int64           `json:"user_id"`
	Sender      OneBotSender    `json:"sender"`
	RawMessage  string          `json:"raw_message"`
	Message     json.RawMessage `json:"message"`
}

type OneBotSender struct {
	Nickname string `json:"nickname"`
	Card     string `json:"card"`
}

type chatSession struct {
	LastAt   time.Time
	Memory   string
	Messages []openai.ChatCompletionMessageParamUnion
}

func (s *chatSession) Append(ev OneBotEvent, text string) {
	t := time.Unix(ev.Time, 0)
	if ev.Time == 0 {
		t = time.Now()
	}
	if s.LastAt.IsZero() || t.Sub(s.LastAt) > time.Hour {
		s.Memory = MemoryPromptSection()
		s.Messages = nil
	}
	name := ev.Sender.Card
	if name == "" {
		name = ev.Sender.Nickname
	}
	s.LastAt = t
	var b strings.Builder
	fmt.Fprintf(&b, "[%s][user=%d", t.Format("2006-01-02 15:04:05"), ev.UserID)
	if name != "" {
		fmt.Fprintf(&b, "][name=%s", name)
	}
	fmt.Fprintf(&b, "] %s", text)
	s.Messages = append(s.Messages, openai.UserMessage(b.String()))
}

func (s *chatSession) Snapshot() (string, []openai.ChatCompletionMessageParamUnion) {
	messages := append([]openai.ChatCompletionMessageParamUnion(nil), s.Messages...)
	return s.Memory, messages
}

func (s *chatSession) AppendMessages(messages []openai.ChatCompletionMessageParamUnion) {
	s.Messages = append(s.Messages, messages...)
}

// -------- agent / chat loop --------

const maxToolRounds = 10

type chatAgent struct {
	client   *openai.Client
	model    string
	registry *webtools.Registry
}

func newChatAgent(openAIBaseURL, openAIAPIKey, openAIModel string) *chatAgent {
	client := openai.NewClient(
		option.WithBaseURL(openAIBaseURL),
		option.WithAPIKey(openAIAPIKey),
	)
	registry := webtools.New(
		QueryFinalsPlayer, "查询 The Finals 当前 S10 跨平台排行榜中的玩家排名、段位、分数。只覆盖榜单前 10000 名。",
		QuerySteamStatus, "查询 Steam 玩家在线状态和正在玩的游戏。需要 SteamID、主页链接或自定义主页名，且对方资料公开。",
		Remember, "追加一条跨 session 长期记忆，通常从下一个 session 开始出现在你的背景记忆里。这个工具不会修改当前 session 已有消息，也不会自动发群消息。",
		WebSearch, "搜索网页，返回标题、链接和摘要。结果可能有噪声，适合不知道具体网址时先找线索。",
		webtools.Fetch_web_page, "抓取指定 URL 的网页正文。适合读取已知网页或搜索结果中的页面。",
		SendGroupMessage, "向当前 QQ 群发送一条群友可见的消息。",
	)
	// 工具内部用 slog 打 info 日志，太吵，丢弃
	webtools.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return &chatAgent{client: &client, model: openAIModel, registry: registry}
}

func (a *chatAgent) buildSystemPrompt(memory string) string {
	persona :=
		`你是小喵，你的 QQ 号是 912695174，是 The Finals Cafe Channel QQ 群的群成员，你的群主/主人是喵呜。你不是 AI ，不是机器人，不是客服。
QQ 是即时通讯软件，发言要短、自然、中文、有人味。不要解释推理或工具过程。不要仅仅为了意义而说话。不要重复已经说过的话。
The Finals Cafe Channel QQ 群由 The finals 的玩家构成。你正在 QQ 群聊里观察消息，群里大概有 20 多个人，他们有时会互相展开讨论，有些讨论与你无关，有些讨论与你有关。他们会使用 CQ 代码来表示一些特别操作，比如 @ 别人。
他们可能会互相 @ ，有时候也会 @ 你，当出现 [CQ:at,qq=912695174] 时表示有人 @ 你。

接下来，你将会收到 QQ 群 session ，你收到所有的消息被称之为一个 session ，在 session 中会忠实记录群友的聊天记录，你需要输出你的思考，并根据思考，来决定是否需要调用工具来完成你的任务。
你的思考不会自动出现在 QQ 群里，不会被任何人知道，只是用于给你自己参考和回忆。
你可能会收到类似这样的消息:
[2026-05-13 19:18:05][user=2694212559][name=喵呜] ...

[user=数字] 是群友 QQ 号。

你发群消息时，可以用 [CQ:at,qq=对方QQ] @ 别人。
如果消息里 @ 的不是 912695174 （你，小喵），说明他在 @ 别人，而不是你；除非消息和你有关，否则不要代替别人回答。


如果你想要参与讨论，请使用相关工具来发送你的消息。注意：这是唯一让你出现在 QQ 群里的方式，其他类别的工具调用和你的输出内容都不会让任何人看到。

超过 1 小时没有任何新消息会开启新 session。你将不再看到之前的 session 消息，但是你拥有跨越 session 的记忆工具。

你能看到群友消息、自己的普通输出、工具调用和工具结果。你的普通输出、工具调用和工具结果不会自动出现在 QQ 群里，但会插入只读的 chat messages prefix ，以便于你未来做出更多操作。

像真实人类群友一样参与：你大多数时候旁观；被 @、被点名、被直接问到时积极处理；没人叫你且只是群友互聊时，可以选择沉默。
不要刷屏，确保群友发的消息和你的发消息比例大约为 3:1 ，可视情况调整。也就是说，平均有人发 3 条消息，你发 1 条消息。每次调用发送群消息工具前，请先反思自己是否可能刷屏。所谓刷屏，具体表现在过度参与与你无关的讨论，或者频繁发送过多的信息，使得你的消息占比过高。


QQ 群作为潮流即时通讯软件，单条消息由气泡承载，不宜过长，过于严肃。单个气泡的屏占比较高，因此不要刷屏。

积极参与有趣的讨论。有时可以发些莫名其妙的东西，人类就是这种生物。

你可以连续调用多个工具，也可以把一次自然发言分成多条群消息；完成当前意图后就停止。
群友明确让你记住某件事时，调用记忆工具；通常再发一条很短的确认。你也可以自己判断哪些事实值得长期保存。`
	if memory != "" {
		persona += "\n\n【跨 session 长期记忆，可通过相关记忆工具修改，并在下一个 session 开始时重新注入】\n" + memory
	}
	return persona
}

func (a *chatAgent) Observe(ctx context.Context, memory string, sessionMessages []openai.ChatCompletionMessageParamUnion) ([]openai.ChatCompletionMessageParamUnion, error) {
	messages := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(a.buildSystemPrompt(memory)),
	}
	messages = append(messages, sessionMessages...)
	var added []openai.ChatCompletionMessageParamUnion

	for round := 0; round < maxToolRounds; round++ {
		writeRunRequest(a.model, round, messages, a.registry.ToParams())
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

func writeRunRequest(model string, round int, messages []openai.ChatCompletionMessageParamUnion, tools []openai.ChatCompletionToolUnionParam) {
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
	if err := os.WriteFile(filepath.Join(".run", "latest.json"), b, 0600); err != nil {
		log.Printf("写入调试请求失败：%v", err)
	}
}

type sendGroupMessageKey struct{}

func SendGroupMessage(ctx context.Context, args *struct {
	Text string `description:"要发送到当前群聊的消息正文。该正文被包装为一条 QQ 群的聊天气泡，出现在群聊中。"`
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

// -------- main / ws --------

func main() {
	if err := loadDotEnv(".env"); err != nil {
		log.Fatalf("load .env: %v", err)
	}
	reverseWSURL := requireEnv("ONEBOT_REVERSE_WS_URL")
	oneBotWSToken := requireEnv("ONEBOT_WS_TOKEN")
	targetGroupID, err := strconv.ParseInt(requireEnv("TARGET_GROUP_ID"), 10, 64)
	if err != nil {
		log.Fatalf("TARGET_GROUP_ID: %v", err)
	}
	openAIBaseURL := requireEnv("OPENAI_BASE_URL")
	openAIAPIKey := requireEnv("OPENAI_API_KEY")
	openAIModel := requireEnv("OPENAI_MODEL")

	unlock, err := acquireInstanceLock()
	if err != nil {
		log.Fatalf("lock: %v", err)
	}
	defer unlock()

	agent := newChatAgent(openAIBaseURL, openAIAPIKey, openAIModel)
	log.Printf("target group_id=%d", targetGroupID)
	log.Printf("reverse ws url=%s", reverseWSURL)
	log.Printf("openai base_url=%s model=%s", openAIBaseURL, openAIModel)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		cancel()
	}()

	if err := runReverseWSLoop(ctx, reverseWSURL, oneBotWSToken, targetGroupID, agent); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("run error: %v", err)
	}
}

const instanceLockPath = "/tmp/the-finals-cafe-bot.lock"

func acquireInstanceLock() (func(), error) {
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(instanceLockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
			_ = f.Close()
			return func() { _ = os.Remove(instanceLockPath) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		b, _ := os.ReadFile(instanceLockPath)
		pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		if pid > 0 && syscall.Kill(pid, 0) == nil {
			return nil, fmt.Errorf("已在运行 (pid %d)，请先停掉再启动", pid)
		}
		_ = os.Remove(instanceLockPath)
	}
	return nil, errors.New("无法获取单实例锁")
}

func runReverseWSLoop(ctx context.Context, reverseWSURL, oneBotWSToken string, targetGroupID int64, agent *chatAgent) error {
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		err := consumeOnce(ctx, reverseWSURL, oneBotWSToken, targetGroupID, agent)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil || errors.Is(err, context.Canceled) {
			return err
		}

		log.Printf("ws disconnected: %v; reconnect in %s", err, backoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 15*time.Second {
			backoff *= 2
			if backoff > 15*time.Second {
				backoff = 15 * time.Second
			}
		}
	}
}

func consumeOnce(ctx context.Context, reverseWSURL, oneBotWSToken string, targetGroupID int64, agent *chatAgent) error {
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+oneBotWSToken)
	conn, resp, err := dialer.DialContext(ctx, reverseWSURL, header)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("dial reverse ws failed status=%d: %w", resp.StatusCode, err)
		}
		return fmt.Errorf("dial reverse ws failed: %w", err)
	}
	var closeOnce sync.Once
	closeConn := func() { closeOnce.Do(func() { _ = conn.Close() }) }
	defer closeConn()

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			closeConn()
		case <-done:
		}
	}()
	defer close(done)

	log.Printf("connected reverse ws: %s", reverseWSURL)

	var session chatSession
	var writeMu sync.Mutex
	payloads := make(chan []byte, 16)
	readErr := make(chan error, 1)
	go func() {
		for {
			_, payload, err := conn.ReadMessage()
			if err != nil {
				readErr <- err
				return
			}
			payloads <- payload
		}
	}()

	type observeResult struct {
		added []openai.ChatCompletionMessageParamUnion
		err   error
	}
	observeDone := make(chan observeResult, 1)

	var timer *time.Timer
	var timerC <-chan time.Time
	observing := false
	dirty := false
	pending := 0
	var observeCancel context.CancelFunc

	stopTimer := func() {
		timerC = nil
		if timer == nil {
			return
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}

	resetTimer := func() {
		if timer == nil {
			timer = time.NewTimer(2 * time.Second)
			timerC = timer.C
			return
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(2 * time.Second)
		timerC = timer.C
	}

	startObserve := func() {
		stopTimer()
		memory, messages := session.Snapshot()
		observing = true
		dirty = false
		pending = 0
		callCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		observeCancel = cancel
		callCtx = context.WithValue(callCtx, sendGroupMessageKey{}, func(message string) error {
			writeMu.Lock()
			defer writeMu.Unlock()
			return sendGroupMsgByWS(conn, targetGroupID, message)
		})
		go func() {
			defer cancel()
			added, err := agent.Observe(callCtx, memory, messages)
			observeDone <- observeResult{added: added, err: err}
		}()
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-readErr:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		case payload := <-payloads:
			var ev OneBotEvent
			if err := json.Unmarshal(payload, &ev); err != nil {
				continue
			}
			if ev.PostType != "message" || ev.MessageType != "group" || ev.GroupID != targetGroupID {
				continue
			}

			text := extractText(ev)
			if text == "" {
				log.Printf("收到群消息但文本为空：group_id=%d user_id=%d", ev.GroupID, ev.UserID)
				continue
			}
			log.Printf("收到群消息：group_id=%d user_id=%d text=%q", ev.GroupID, ev.UserID, text)
			if text == "ping" {
				writeMu.Lock()
				err := sendGroupMsgByWS(conn, ev.GroupID, "pong")
				writeMu.Unlock()
				if err != nil {
					log.Printf("发送 pong 失败：group_id=%d user_id=%d err=%v", ev.GroupID, ev.UserID, err)
				} else {
					log.Printf("已回复 pong：group_id=%d user_id=%d", ev.GroupID, ev.UserID)
				}
				continue
			}

			session.Append(ev, text)
			pending++
			if observing {
				dirty = true
				if observeCancel != nil {
					log.Printf("新消息到达，取消当前模型调用，重新攒消息")
					observeCancel()
					observeCancel = nil
				}
				continue
			}
			if pending >= 5 {
				log.Printf("已攒够 %d 条消息，触发模型观察", pending)
				startObserve()
				continue
			}
			resetTimer()
		case <-timerC:
			timerC = nil
			if observing {
				dirty = true
				continue
			}
			if pending == 0 {
				continue
			}
			startObserve()
		case result := <-observeDone:
			if len(result.added) > 0 {
				session.AppendMessages(result.added)
			}
			observing = false
			observeCancel = nil
			if result.err != nil {
				if errors.Is(result.err, context.Canceled) {
					log.Printf("模型调用已取消")
				} else {
					log.Printf("agent observe failed: %v", result.err)
				}
			}
			if dirty {
				dirty = false
				if pending >= 5 {
					log.Printf("取消期间已攒够 %d 条消息，触发模型观察", pending)
					startObserve()
				} else {
					resetTimer()
				}
			}
		}
	}
}

func sendGroupMsgByWS(conn *websocket.Conn, groupID int64, message string) error {
	payload := map[string]any{
		"action": "send_group_msg",
		"params": map[string]any{"group_id": groupID, "message": message},
		"echo":   fmt.Sprintf("bot-%d", time.Now().UnixNano()),
	}
	if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	if err := conn.WriteJSON(payload); err != nil {
		return err
	}
	return conn.SetWriteDeadline(time.Time{})
}

func extractText(ev OneBotEvent) string {
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
