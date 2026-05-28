package app

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

const (
	maintainerQQ = 2694212559

	// 群里管理指令：@ 机器人后 /ping、/hang、/bill（前后空白会 TrimSpace）
	cmdPing  = "/ping"
	cmdHang  = "/hang"
	cmdBill  = "/bill"

	sessionPauseSummaryEvent = "【系统事件】本 session 即将结束。请简要总结本轮对话要点；如有值得跨 session 保存的信息，可调用记忆工具更新记忆。除非非常必要，不要发送群消息。"
)

func Run(systemPrompt string, exampleConfig []byte) {
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
	if err := ensureConfig("go.toml", exampleConfig); err != nil {
		log.Fatalf("ensure go.toml: %v", err)
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

	agent := newChatAgent(openAIBaseURL, openAIAPIKey, openAIModel, systemPrompt)
	log.Printf("target groups=%v", groupIDs(groups))
	log.Printf("reverse ws url=%s", cfg.OneBot.ReverseWSURL)
	log.Printf("openai base_url=%s model=%s", openAIBaseURL, openAIModel)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sig)

	if err := runReverseWSLoop(ctx, sig, "go.toml", cfg, groups, agent); err != nil && !errors.Is(err, context.Canceled) {
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

func runReverseWSLoop(ctx context.Context, sig <-chan os.Signal, configPath string, cfg *botConfig, groups map[int64]*groupState, agent *chatAgent) error {
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-sig:
			return context.Canceled
		default:
		}

		err := consumeOnce(ctx, sig, configPath, cfg, groups, agent)
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
		case <-sig:
			return context.Canceled
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

func consumeOnce(ctx context.Context, sig <-chan os.Signal, configPath string, cfg *botConfig, groups map[int64]*groupState, agent *chatAgent) error {
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
	botQQ, err := getLoginQQByWS(conn)
	if err != nil {
		return err
	}
	log.Printf("登录 QQ=%d", botQQ)
	atPrefix := "[CQ:at,qq=" + strconv.FormatInt(botQQ, 10) + "]"

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
		groupID       int64
		generation    int64
		added         []openai.ChatCompletionMessageParamUnion
		err           error
		pauseSummary  bool
		costYuan      float64
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
		delay := coldDelay(group.Runtime.Pending, group.Runtime.PendingAtBot) - time.Since(group.Runtime.PendingLastAt)
		if delay < 0 {
			delay = 0
		}
		group.Runtime.Timer = time.AfterFunc(delay, func() {
			timerDone <- timerEvent{groupID: group.ID, seq: seq}
		})
	}

	var (
		pauseSession func(group *groupState)
		startObserve func(group *groupState)
	)

	observeContext := func(group *groupState, timeout time.Duration) (context.Context, context.CancelFunc) {
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		var usageAcc observeUsageAcc
		usageAcc.GroupID = group.ID
		callCtx = withObserveUsage(callCtx, &usageAcc)
		callCtx = bottools.WithGroupID(callCtx, group.ID)
		callCtx = bottools.WithMemoryFile(callCtx, group.MemoryFile)
		callCtx = bottools.WithSendGroupMessage(callCtx, func(message string) error {
			writeMu.Lock()
			defer writeMu.Unlock()
			return sendGroupMsgByWS(conn, group.ID, message)
		})
		callCtx = bottools.WithSendGroupTempPrivate(callCtx, func(userID int64, message string) error {
			writeMu.Lock()
			defer writeMu.Unlock()
			err := sendPrivateMsgByWS(conn, userID, group.ID, message)
			if err != nil {
				log.Printf("模型发送群临时私聊失败：group_id=%d user_id=%d err=%v", group.ID, userID, err)
				return err
			}
			log.Printf("模型发送群临时私聊：group_id=%d user_id=%d text=%q", group.ID, userID, truncateForLog(message, 200))
			return nil
		})
		callCtx = bottools.WithGroupMembers(callCtx, func(ctx context.Context, query string, limit int) (string, error) {
			return queryGroupMembersByWS(ctx, callOneBot, group.ID, query, limit)
		})
		callCtx = bottools.WithGroupPoke(callCtx, func(userID int64) error {
			writeMu.Lock()
			defer writeMu.Unlock()
			return pokeGroupMemberByWS(conn, group.ID, userID)
		})
		callCtx = withTokenUsageReporter(callCtx, func(usage tokenUsage) {
			usage = accumulateUsage(callCtx, usage)
			logUsageRecord(usage)
			group.Runtime.Session.CostYuan += usage.CostYuan
			if group.Runtime.PauseRunning || group.Runtime.PausePending {
				return
			}
			if group.Runtime.Session.CostYuan >= sessionCostLimitYuan {
				log.Printf("群 %d session 累计费用 %.4f 元达到上限 %.2f 元，结束 session 并总结", group.ID, group.Runtime.Session.CostYuan, sessionCostLimitYuan)
				pauseSession(group)
			}
		})
		return callCtx, cancel
	}

	finishPause := func(group *groupState, costYuan float64) {
		stopTimer(group)
		group.Runtime.PauseRunning = false
		group.Runtime.Observing = false
		group.Runtime.ObserveCancel = nil
		group.Runtime.Dirty = false
		pending := group.Runtime.Pending
		group.Runtime.Session.ClearContext(group.MemoryFile)
		log.Printf("群 %d session 已结束，本 session 累计 %.4f 元", group.ID, costYuan)
		if pending > 0 || group.Runtime.Session.PendingUserText.Len() > 0 {
			log.Printf("群 %d session 结束，%d 条待处理消息，立即开新 session", group.ID, pending)
			startObserve(group)
		}
	}

	runPauseSummary := func(group *groupState, memory string, messages []openai.ChatCompletionMessageParamUnion) {
		costYuan := group.Runtime.Session.CostYuan
		stopTimer(group)
		group.Runtime.PauseRunning = true
		group.Runtime.Observing = true
		group.Runtime.Dirty = false
		generation := group.Runtime.Generation
		messages = appendObserveReminder(messages)
		messages = append(messages, openai.UserMessage(sessionPauseSummaryEvent))
		callCtx, cancel := observeContext(group, 90*time.Second)
		group.Runtime.ObserveCancel = cancel
		go func() {
			defer cancel()
			_, err := agent.Observe(callCtx, group.ID, botQQ, group.Prompt, memory, messages)
			observeDone <- observeResult{groupID: group.ID, generation: generation, pauseSummary: true, costYuan: costYuan, err: err}
		}()
	}

	execPause := func(group *groupState) {
		writeMu.Lock()
		err := sendGroupMsgByWS(conn, group.ID, "【有点累了喵，正在放慢脚步重新整理记忆，短期内可能会失明】")
		writeMu.Unlock()
		if err != nil {
			log.Printf("发送 session 挂起群提示失败：group_id=%d err=%v", group.ID, err)
		}
		memory, messages := group.Runtime.Session.Snapshot()
		if len(messages) == 0 {
			finishPause(group, group.Runtime.Session.CostYuan)
			return
		}
		runPauseSummary(group, memory, messages)
	}

	pauseSession = func(group *groupState) {
		if group.Runtime.PauseRunning || group.Runtime.PausePending {
			return
		}
		stopTimer(group)
		if group.Runtime.Observing && group.Runtime.ObserveCancel != nil {
			group.Runtime.PausePending = true
			group.Runtime.ObserveCancel()
			return
		}
		group.Runtime.Generation++
		execPause(group)
	}

	finalObserve := func(group *groupState) {
		stopTimer(group)
		memory, messages := group.Runtime.Session.Snapshot()
		if len(messages) == 0 {
			return
		}
		messages = appendObserveReminder(messages)
		messages = append(messages, openai.UserMessage("【系统事件】当前 session 因为 20 分钟没有新消息即将结束。你可以做最后一次观察；如果有值得跨 session 保存的信息，可以调用记忆工具更新记忆。除非非常必要，不需要发送群消息。"))
		callCtx, cancel := observeContext(group, 90*time.Second)
		defer cancel()
		if _, err := agent.Observe(callCtx, group.ID, botQQ, group.Prompt, memory, messages); err != nil {
			log.Printf("final observe failed: group_id=%d err=%v", group.ID, err)
		}
	}

	gracefulShutdown := func() {
		for _, group := range groups {
			if !group.Runtime.Session.HasContent() {
				continue
			}
			stopTimer(group)
			if group.Runtime.ObserveCancel != nil {
				group.Runtime.ObserveCancel()
				group.Runtime.ObserveCancel = nil
				group.Runtime.Observing = false
			}
			writeMu.Lock()
			_ = sendGroupMsgByWS(conn, group.ID, "【有点累了喵，正在放慢脚步重新整理记忆，短期内可能会失明】")
			writeMu.Unlock()
			finalObserve(group)
		}
	}

	startObserve = func(group *groupState) {
		if group.Runtime.PauseRunning || group.Runtime.PausePending {
			return
		}
		stopTimer(group)
		memory, messages := group.Runtime.Session.Snapshot()
		messages = appendObserveReminder(messages)
		group.Runtime.Observing = true
		group.Runtime.Dirty = false
		group.Runtime.Pending = 0
		group.Runtime.PendingAtBot = false
		generation := group.Runtime.Generation
		callCtx, cancel := observeContext(group, 90*time.Second)
		group.Runtime.ObserveCancel = cancel
		go func() {
			defer cancel()
			added, err := agent.Observe(callCtx, group.ID, botQQ, group.Prompt, memory, messages)
			observeDone <- observeResult{groupID: group.ID, generation: generation, added: added, err: err}
		}()
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-sig:
			log.Printf("收到退出信号，开始整理 session 记忆")
			gracefulShutdown()
			return context.Canceled
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

			text := strings.TrimSpace(extractText(ev))
			if text == "" {
				log.Printf("收到群消息但文本为空：group_id=%d user_id=%d", ev.GroupID, ev.UserID)
				continue
			}
			incoming := RunIncomingHandlers(IncomingGroupMessage{Event: ev, Text: text}, AtBotHandler(botQQ))
			log.Printf("收到群消息：group_id=%d user_id=%d at_bot=%v text=%q", ev.GroupID, ev.UserID, incoming.AtBot, text)
			if strings.HasPrefix(text, atPrefix) {
				switch strings.TrimSpace(text[len(atPrefix):]) {
				case cmdPing:
					writeMu.Lock()
					err := sendGroupMsgByWS(conn, ev.GroupID, "pong")
					writeMu.Unlock()
					if err != nil {
						log.Printf("发送 pong 失败：group_id=%d user_id=%d err=%v", ev.GroupID, ev.UserID, err)
					} else {
						log.Printf("已回复 pong：group_id=%d user_id=%d", ev.GroupID, ev.UserID)
					}
					continue
				case cmdHang:
					log.Printf("群 %d 收到 /hang 指令：user_id=%d", ev.GroupID, ev.UserID)
					pauseSession(group)
					continue
				case cmdBill:
					msg, err := bottools.GroupBillText(ev.GroupID)
					if err != nil {
						msg = fmt.Sprintf("查询账单失败：%v", err)
					}
					writeMu.Lock()
					err = sendPrivateMsgByWS(conn, ev.UserID, ev.GroupID, msg)
					writeMu.Unlock()
					if err != nil {
						log.Printf("私发账单失败：group_id=%d user_id=%d err=%v", ev.GroupID, ev.UserID, err)
					} else {
						log.Printf("已私发账单：group_id=%d user_id=%d", ev.GroupID, ev.UserID)
					}
					continue
				}
			}

			t := eventTime(ev)
			if !group.Runtime.Observing && group.Runtime.Session.ExpiredAt(t) {
				log.Printf("群 %d 当前 session 超过 20 分钟无新消息，触发 final observe", group.ID)
				finalObserve(group)
				group.Runtime = groupRuntime{Generation: group.Runtime.Generation + 1}
			}
			group.Runtime.Session.Append(ev, text, group.MemoryFile)
			group.Runtime.Pending++
			group.Runtime.PendingLastAt = time.Now()
			if incoming.AtBot {
				group.Runtime.PendingAtBot = true
			}
			if group.Runtime.Observing {
				group.Runtime.Dirty = true
				continue
			}
			if group.Runtime.Pending >= 5 {
				log.Printf("群 %d 已攒够 %d 条消息，触发模型观察", group.ID, group.Runtime.Pending)
				startObserve(group)
				continue
			}
			if group.Runtime.PendingAtBot {
				log.Printf("群 %d 消息 @ 了 bot，立即触发模型观察", group.ID)
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
			if result.pauseSummary {
				finishPause(group, result.costYuan)
				continue
			}
			if len(result.added) > 0 && !group.Runtime.PausePending {
				group.Runtime.Session.AppendMessages(result.added)
			}
			group.Runtime.Observing = false
			group.Runtime.ObserveCancel = nil
			if group.Runtime.PausePending {
				group.Runtime.PausePending = false
				group.Runtime.Generation++
				execPause(group)
				continue
			}
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
				} else if group.Runtime.PendingAtBot {
					log.Printf("群 %d 观察期间收到 @，立即再次观察", group.ID)
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
			reloadGroups(groups, next, stopTimer, finalObserve, func(groupID int64, userID int64) {
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

func reloadGroups(groups map[int64]*groupState, cfg *botConfig, stopTimer func(*groupState), finalObserve func(*groupState), pokeMaintainer func(int64, int64)) {
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
		if !group.Runtime.Observing {
			finalObserve(group)
		}
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
		if promptChanged || memoryChanged {
			stopTimer(group)
			if !group.Runtime.Observing {
				finalObserve(group)
			}
			if group.Runtime.ObserveCancel != nil {
				group.Runtime.ObserveCancel()
			}
			group.Prompt = nextGroup.Prompt
			group.MemoryFile = nextGroup.MemoryFile
			group.Runtime = groupRuntime{Generation: group.Runtime.Generation + 1}
			log.Printf("群 %d 配置已变更，已重启当前对话", id)
			pokeMaintainer(id, maintainerQQ)
		}
	}
}
