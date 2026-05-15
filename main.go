package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/eastlaugh/gobot/internal/bottools"
	"github.com/fsnotify/fsnotify"
	"github.com/gorilla/websocket"
	"github.com/openai/openai-go/v3"
)

var oneBotWSToken string

const maintainerQQ = 2694212559

func loadDotEnv(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.Index(line, "=")
		if i <= 0 {
			return fmt.Errorf("%s: invalid env line: %q", path, line)
		}
		if err := os.Setenv(line[:i], line[i+1:]); err != nil {
			return err
		}
	}
	return nil
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
	LastAt          time.Time
	Memory          string
	Messages        []openai.ChatCompletionMessageParamUnion
	PendingUserText strings.Builder
}

type groupRuntime struct {
	Session       chatSession
	Timer         *time.Timer
	TimerSeq      int64
	Observing     bool
	Dirty         bool
	Pending       int
	PendingLastAt time.Time
	ObserveCancel context.CancelFunc
	Generation    int64
}

func (s *chatSession) Append(ev OneBotEvent, text string, memoryFile string) {
	t := time.Unix(ev.Time, 0)
	if ev.Time == 0 {
		t = time.Now()
	}
	if s.LastAt.IsZero() || t.Sub(s.LastAt) > time.Hour {
		s.Memory = bottools.MemoryPromptSection(memoryFile)
		s.Messages = nil
		s.PendingUserText.Reset()
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
	if s.PendingUserText.Len() > 0 {
		s.PendingUserText.WriteString("\n")
	}
	s.PendingUserText.WriteString(b.String())
}

func (s *chatSession) Snapshot() (string, []openai.ChatCompletionMessageParamUnion) {
	messages := append([]openai.ChatCompletionMessageParamUnion(nil), s.Messages...)
	if s.PendingUserText.Len() > 0 {
		messages = append(messages, openai.UserMessage(s.PendingUserText.String()))
	}
	return s.Memory, messages
}

func (s *chatSession) AppendMessages(messages []openai.ChatCompletionMessageParamUnion) {
	if s.PendingUserText.Len() > 0 {
		s.Messages = append(s.Messages, openai.UserMessage(s.PendingUserText.String()))
		s.PendingUserText.Reset()
	}
	s.Messages = append(s.Messages, messages...)
}

// -------- main / ws --------

func main() {
	if err := loadDotEnv(".env"); err != nil {
		log.Fatalf("load .env: %v", err)
	}
	oneBotWSToken = os.Getenv("ONEBOT_WS_TOKEN")
	openAIAPIKey := os.Getenv("OPENAI_API_KEY")
	openAIBaseURL := os.Getenv("OPENAI_BASE_URL")
	openAIModel := os.Getenv("OPENAI_MODEL")
	steamAPIKey := os.Getenv("STEAM_API_KEY")
	if oneBotWSToken == "" || openAIAPIKey == "" || openAIBaseURL == "" || openAIModel == "" || steamAPIKey == "" {
		panic("ONEBOT_WS_TOKEN / OPENAI_API_KEY / OPENAI_BASE_URL / OPENAI_MODEL / STEAM_API_KEY is required")
	}
	cfg, err := loadBotConfig("go.toml")
	if err != nil {
		log.Fatalf("load go.toml: %v", err)
	}
	groups, err := loadGroupStates(cfg)
	if err != nil {
		log.Fatalf("load groups: %v", err)
	}

	unlock, err := acquireInstanceLock()
	if err != nil {
		log.Fatalf("lock: %v", err)
	}
	defer unlock()

	agent := newChatAgent(openAIBaseURL, openAIAPIKey, openAIModel)
	log.Printf("target groups=%v", groupIDs(groups))
	log.Printf("reverse ws url=%s", cfg.OneBot.ReverseWSURL)
	log.Printf("openai base_url=%s model=%s", openAIBaseURL, openAIModel)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		cancel()
	}()

	if err := runReverseWSLoop(ctx, "go.toml", cfg, groups, agent); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("run error: %v", err)
	}
}

const instanceLockPath = "/tmp/gobot.lock"

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

func groupIDs(groups map[int64]*groupState) []int64 {
	ids := make([]int64, 0, len(groups))
	for id := range groups {
		ids = append(ids, id)
	}
	return ids
}

func runReverseWSLoop(ctx context.Context, configPath string, cfg *botConfig, groups map[int64]*groupState, agent *chatAgent) error {
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		err := consumeOnce(ctx, configPath, cfg, groups, agent)
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

func consumeOnce(ctx context.Context, configPath string, cfg *botConfig, groups map[int64]*groupState, agent *chatAgent) error {
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+oneBotWSToken)
	conn, resp, err := dialer.DialContext(ctx, cfg.OneBot.ReverseWSURL, header)
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

	log.Printf("connected reverse ws: %s", cfg.OneBot.ReverseWSURL)

	var writeMu sync.Mutex
	var rpcMu sync.Mutex
	rpcPending := map[string]chan []byte{}
	callOneBot := func(ctx context.Context, action string, params map[string]any) ([]byte, error) {
		echo := fmt.Sprintf("rpc-%d", time.Now().UnixNano())
		ch := make(chan []byte, 1)
		rpcMu.Lock()
		rpcPending[echo] = ch
		rpcMu.Unlock()
		defer func() {
			rpcMu.Lock()
			delete(rpcPending, echo)
			rpcMu.Unlock()
		}()

		writeMu.Lock()
		err := conn.WriteJSON(map[string]any{
			"action": action,
			"params": params,
			"echo":   echo,
		})
		writeMu.Unlock()
		if err != nil {
			return nil, err
		}

		select {
		case b := <-ch:
			return b, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
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
		groupID    int64
		generation int64
		added      []openai.ChatCompletionMessageParamUnion
		err        error
	}
	type timerEvent struct {
		groupID int64
		seq     int64
	}
	observeDone := make(chan observeResult, 16)
	timerDone := make(chan timerEvent, 64)
	configChanged := make(chan struct{}, 1)
	watcher, err := watchConfig(ctx, configPath, configChanged)
	if err != nil {
		return err
	}
	defer watcher.Close()

	stopTimer := func(group *groupState) {
		if group.Runtime.Timer != nil {
			group.Runtime.Timer.Stop()
			group.Runtime.Timer = nil
		}
	}

	resetTimer := func(group *groupState) {
		stopTimer(group)
		group.Runtime.TimerSeq++
		seq := group.Runtime.TimerSeq
		delay := coldDelay(group.Runtime.Pending) - time.Since(group.Runtime.PendingLastAt)
		if delay < 0 {
			delay = 0
		}
		group.Runtime.Timer = time.AfterFunc(delay, func() {
			timerDone <- timerEvent{groupID: group.ID, seq: seq}
		})
	}

	startObserve := func(group *groupState) {
		stopTimer(group)
		memory, messages := group.Runtime.Session.Snapshot()
		group.Runtime.Observing = true
		group.Runtime.Dirty = false
		group.Runtime.Pending = 0
		generation := group.Runtime.Generation
		callCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		group.Runtime.ObserveCancel = cancel
		callCtx = bottools.WithMemoryFile(callCtx, group.MemoryFile)
		callCtx = bottools.WithSendGroupMessage(callCtx, func(message string) error {
			writeMu.Lock()
			defer writeMu.Unlock()
			return sendGroupMsgByWS(conn, group.ID, message)
		})
		callCtx = bottools.WithGroupMembers(callCtx, func(ctx context.Context, query string, limit int) (string, error) {
			return queryGroupMembersByWS(ctx, callOneBot, group.ID, query, limit)
		})
		callCtx = bottools.WithGroupPoke(callCtx, func(userID int64) error {
			writeMu.Lock()
			defer writeMu.Unlock()
			return pokeGroupMemberByWS(conn, group.ID, userID)
		})
		go func() {
			defer cancel()
			added, err := agent.Observe(callCtx, group.ID, group.Prompt, memory, messages)
			observeDone <- observeResult{groupID: group.ID, generation: generation, added: added, err: err}
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
			var rpc struct {
				Echo json.RawMessage `json:"echo"`
			}
			if err := json.Unmarshal(payload, &rpc); err == nil && len(rpc.Echo) > 0 {
				var echo string
				if err := json.Unmarshal(rpc.Echo, &echo); err == nil {
					rpcMu.Lock()
					ch := rpcPending[echo]
					rpcMu.Unlock()
					if ch != nil {
						ch <- payload
						continue
					}
				}
			}

			var ev OneBotEvent
			if err := json.Unmarshal(payload, &ev); err != nil {
				continue
			}
			if ev.PostType != "message" || ev.MessageType != "group" {
				continue
			}
			group := groups[ev.GroupID]
			if group == nil {
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

			group.Runtime.Session.Append(ev, text, group.MemoryFile)
			group.Runtime.Pending++
			group.Runtime.PendingLastAt = time.Now()
			if group.Runtime.Observing {
				group.Runtime.Dirty = true
				continue
			}
			if group.Runtime.Pending >= 5 {
				log.Printf("群 %d 已攒够 %d 条消息，触发模型观察", group.ID, group.Runtime.Pending)
				startObserve(group)
				continue
			}
			resetTimer(group)
		case event := <-timerDone:
			group := groups[event.groupID]
			if group == nil || event.seq != group.Runtime.TimerSeq {
				continue
			}
			group.Runtime.Timer = nil
			if group.Runtime.Observing {
				group.Runtime.Dirty = true
				continue
			}
			if group.Runtime.Pending == 0 {
				continue
			}
			startObserve(group)
		case result := <-observeDone:
			group := groups[result.groupID]
			if group == nil || result.generation != group.Runtime.Generation {
				continue
			}
			if len(result.added) > 0 {
				group.Runtime.Session.AppendMessages(result.added)
			}
			group.Runtime.Observing = false
			group.Runtime.ObserveCancel = nil
			if result.err != nil {
				if errors.Is(result.err, context.Canceled) {
					log.Printf("模型调用已取消")
				} else {
					log.Printf("agent observe failed: %v", result.err)
				}
			}
			if group.Runtime.Dirty {
				group.Runtime.Dirty = false
				if group.Runtime.Pending >= 5 {
					log.Printf("群 %d 取消期间已攒够 %d 条消息，触发模型观察", group.ID, group.Runtime.Pending)
					startObserve(group)
				} else {
					resetTimer(group)
				}
			}
		case <-configChanged:
			next, err := loadBotConfig(configPath)
			if err != nil {
				log.Printf("reload go.toml failed: %v", err)
				continue
			}
			reloadGroups(groups, next, stopTimer, func(groupID int64, userID int64) {
				writeMu.Lock()
				defer writeMu.Unlock()
				if err := pokeGroupMemberByWS(conn, groupID, userID); err != nil {
					log.Printf("发送配置变更戳一戳失败：group_id=%d user_id=%d err=%v", groupID, userID, err)
				}
			})
			cfg = next
		}
	}
}

func coldDelay(pending int) time.Duration {
	if pending == 1 {
		return 6 * time.Second
	}
	if pending == 2 {
		return 15 * time.Second
	}
	return 30 * time.Second
}

func watchConfig(ctx context.Context, path string, changed chan<- struct{}) (*fsnotify.Watcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	name := filepath.Base(path)
	if err := watcher.Add(dir); err != nil {
		_ = watcher.Close()
		return nil, err
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if filepath.Base(event.Name) != name {
					continue
				}
				if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) || event.Has(fsnotify.Rename) {
					select {
					case changed <- struct{}{}:
					default:
					}
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Printf("watch go.toml failed: %v", err)
			}
		}
	}()
	return watcher, nil
}

func reloadGroups(groups map[int64]*groupState, cfg *botConfig, stopTimer func(*groupState), pokeMaintainer func(int64, int64)) {
	next, err := loadGroupStates(cfg)
	if err != nil {
		log.Printf("reload groups failed: %v", err)
		return
	}
	for id, group := range groups {
		if next[id] != nil {
			continue
		}
		stopTimer(group)
		if group.Runtime.ObserveCancel != nil {
			group.Runtime.ObserveCancel()
		}
		delete(groups, id)
		log.Printf("群 %d 已从 go.toml 移除", id)
	}
	for id, nextGroup := range next {
		group := groups[id]
		if group == nil {
			groups[id] = nextGroup
			log.Printf("群 %d 已加入 go.toml", id)
			continue
		}
		promptChanged := group.Prompt != nextGroup.Prompt
		memoryChanged := group.MemoryFile != nextGroup.MemoryFile
		group.Prompt = nextGroup.Prompt
		group.MemoryFile = nextGroup.MemoryFile
		if promptChanged || memoryChanged {
			stopTimer(group)
			if group.Runtime.ObserveCancel != nil {
				group.Runtime.ObserveCancel()
			}
			group.Runtime = groupRuntime{Generation: group.Runtime.Generation + 1}
			log.Printf("群 %d 配置已变更，已重启当前对话", id)
			pokeMaintainer(id, maintainerQQ)
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

func pokeGroupMemberByWS(conn *websocket.Conn, groupID int64, userID int64) error {
	payload := map[string]any{
		"action": "group_poke",
		"params": map[string]any{"group_id": groupID, "user_id": userID},
		"echo":   fmt.Sprintf("poke-%d", time.Now().UnixNano()),
	}
	if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	if err := conn.WriteJSON(payload); err != nil {
		return err
	}
	return conn.SetWriteDeadline(time.Time{})
}

type oneBotMember struct {
	UserID   int64  `json:"user_id"`
	Nickname string `json:"nickname"`
	Card     string `json:"card"`
	Role     string `json:"role"`
	Title    string `json:"title"`
}

func queryGroupMembersByWS(ctx context.Context, callOneBot func(context.Context, string, map[string]any) ([]byte, error), groupID int64, query string, limit int) (string, error) {
	b, err := callOneBot(ctx, "get_group_member_list", map[string]any{"group_id": groupID})
	if err != nil {
		return "", err
	}
	var resp struct {
		Status  string         `json:"status"`
		Retcode int            `json:"retcode"`
		Message string         `json:"message"`
		Data    []oneBotMember `json:"data"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return "", err
	}
	if resp.Status != "ok" || resp.Retcode != 0 {
		return "", fmt.Errorf("OneBot get_group_member_list failed: status=%s retcode=%d message=%s", resp.Status, resp.Retcode, resp.Message)
	}
	if limit <= 0 {
		limit = 50
	}
	query = strings.ToLower(query)
	var members []oneBotMember
	for _, m := range resp.Data {
		if query != "" && !strings.Contains(strings.ToLower(fmt.Sprint(m.UserID)), query) && !strings.Contains(strings.ToLower(m.Nickname), query) && !strings.Contains(strings.ToLower(m.Card), query) {
			continue
		}
		members = append(members, m)
	}
	slices.SortFunc(members, func(a, b oneBotMember) int {
		return strings.Compare(displayMemberName(a), displayMemberName(b))
	})
	if len(members) == 0 {
		return "没有匹配的群成员。", nil
	}

	var out strings.Builder
	fmt.Fprintf(&out, "当前群匹配到 %d 个成员", len(members))
	if len(members) > limit {
		fmt.Fprintf(&out, "，只显示前 %d 个", limit)
	}
	out.WriteString("：\n")
	for i, m := range members {
		if i >= limit {
			break
		}
		fmt.Fprintf(&out, "- %d | %s", m.UserID, displayMemberName(m))
		if m.Role != "" {
			fmt.Fprintf(&out, " | %s", m.Role)
		}
		if m.Title != "" {
			fmt.Fprintf(&out, " | %s", m.Title)
		}
		out.WriteString("\n")
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

func displayMemberName(m oneBotMember) string {
	if m.Card != "" {
		return m.Card
	}
	if m.Nickname != "" {
		return m.Nickname
	}
	return fmt.Sprint(m.UserID)
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
