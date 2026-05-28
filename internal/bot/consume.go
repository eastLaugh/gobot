package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/eastlaugh/gobot/internal/agent"
	"github.com/eastlaugh/gobot/internal/bottools"
	"github.com/eastlaugh/gobot/internal/config"
	"github.com/eastlaugh/gobot/internal/incoming"
	"github.com/eastlaugh/gobot/internal/onebot"
	"github.com/eastlaugh/gobot/internal/session"
	"github.com/gorilla/websocket"
	"github.com/openai/openai-go/v3"
)

func (b *Bot) consumeOnce(ctx context.Context, sig <-chan os.Signal) error {
	cfg := b.cfg
	groups := b.groups
	ag := b.agent
	configPath := b.configPath

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+b.wsToken)
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
	botQQ, err := onebot.LoginQQ(conn)
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

	stopTimer := func(group *session.Group) {
		if group.Runtime.Timer != nil {
			group.Runtime.Timer.Stop()
			group.Runtime.Timer = nil
		}
	}

	resetTimer := func(group *session.Group) {
		stopTimer(group)
		group.Runtime.TimerSeq++
		seq := group.Runtime.TimerSeq
		delay := session.ColdDelay(group.Runtime.Pending, group.Runtime.PendingAtBot) - time.Since(group.Runtime.PendingLastAt)
		if delay < 0 {
			delay = 0
		}
		group.Runtime.Timer = time.AfterFunc(delay, func() {
			timerDone <- timerEvent{groupID: group.ID, seq: seq}
		})
	}

	var (
		pauseSession func(group *session.Group)
		startObserve func(group *session.Group)
	)

	observeContext := func(group *session.Group, timeout time.Duration) (context.Context, context.CancelFunc) {
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		var usageAcc agent.ObserveUsageAcc
		usageAcc.GroupID = group.ID
		callCtx = agent.WithObserveUsage(callCtx, &usageAcc)
		callCtx = bottools.WithGroupID(callCtx, group.ID)
		callCtx = bottools.WithMemoryFile(callCtx, group.MemoryFile)
		callCtx = bottools.WithSendGroupMessage(callCtx, func(message string) error {
			writeMu.Lock()
			defer writeMu.Unlock()
			return onebot.SendGroupMsg(conn, group.ID, message)
		})
		callCtx = bottools.WithSendGroupTempPrivate(callCtx, func(userID int64, message string) error {
			writeMu.Lock()
			defer writeMu.Unlock()
			err := onebot.SendPrivateMsg(conn, userID, group.ID, message)
			if err != nil {
				log.Printf("模型发送群临时私聊失败：group_id=%d user_id=%d err=%v", group.ID, userID, err)
				return err
			}
			log.Printf("模型发送群临时私聊：group_id=%d user_id=%d text=%q", group.ID, userID, agent.TruncateForLog(message, 200))
			return nil
		})
		callCtx = bottools.WithGroupMembers(callCtx, func(ctx context.Context, query string, limit int) (string, error) {
			return onebot.QueryGroupMembers(ctx, callOneBot, group.ID, query, limit)
		})
		callCtx = bottools.WithGroupPoke(callCtx, func(userID int64) error {
			writeMu.Lock()
			defer writeMu.Unlock()
			return onebot.PokeGroupMember(conn, group.ID, userID)
		})
		callCtx = agent.WithTokenUsageReporter(callCtx, func(usage agent.TokenUsage) {
			usage = agent.AccumulateUsage(callCtx, usage)
			agent.LogUsageRecord(usage)
			group.Runtime.Session.CostYuan += usage.CostYuan
			if group.Runtime.PauseRunning || group.Runtime.PausePending {
				return
			}
			if group.Runtime.Session.CostYuan >= session.CostLimitYuan {
				log.Printf("群 %d session 累计费用 %.4f 元达到上限 %.2f 元，结束 session 并总结", group.ID, group.Runtime.Session.CostYuan, session.CostLimitYuan)
				pauseSession(group)
			}
		})
		return callCtx, cancel
	}

	finishPause := func(group *session.Group, costYuan float64) {
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

	runPauseSummary := func(group *session.Group, memory string, messages []openai.ChatCompletionMessageParamUnion) {
		costYuan := group.Runtime.Session.CostYuan
		stopTimer(group)
		group.Runtime.PauseRunning = true
		group.Runtime.Observing = true
		group.Runtime.Dirty = false
		generation := group.Runtime.Generation
		messages = session.AppendObserveReminder(messages)
		messages = append(messages, openai.UserMessage(sessionPauseSummaryEvent))
		callCtx, cancel := observeContext(group, 90*time.Second)
		group.Runtime.ObserveCancel = cancel
		go func() {
			defer cancel()
			_, err := ag.Observe(callCtx, group.ID, botQQ, group.Prompt, memory, messages)
			observeDone <- observeResult{groupID: group.ID, generation: generation, pauseSummary: true, costYuan: costYuan, err: err}
		}()
	}

	execPause := func(group *session.Group) {
		writeMu.Lock()
		err := onebot.SendGroupMsg(conn, group.ID, "【有点累了喵，正在放慢脚步重新整理记忆，短期内可能会失明】")
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

	pauseSession = func(group *session.Group) {
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

	finalObserve := func(group *session.Group) {
		stopTimer(group)
		memory, messages := group.Runtime.Session.Snapshot()
		if len(messages) == 0 {
			return
		}
		messages = session.AppendObserveReminder(messages)
		messages = append(messages, openai.UserMessage("【系统事件】当前 session 因为 20 分钟没有新消息即将结束。你可以做最后一次观察；如果有值得跨 session 保存的信息，可以调用记忆工具更新记忆。除非非常必要，不需要发送群消息。"))
		callCtx, cancel := observeContext(group, 90*time.Second)
		defer cancel()
		if _, err := ag.Observe(callCtx, group.ID, botQQ, group.Prompt, memory, messages); err != nil {
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
			_ = onebot.SendGroupMsg(conn, group.ID, "【有点累了喵，正在放慢脚步重新整理记忆，短期内可能会失明】")
			writeMu.Unlock()
			finalObserve(group)
		}
	}

	startObserve = func(group *session.Group) {
		if group.Runtime.PauseRunning || group.Runtime.PausePending {
			return
		}
		stopTimer(group)
		memory, messages := group.Runtime.Session.Snapshot()
		messages = session.AppendObserveReminder(messages)
		group.Runtime.Observing = true
		group.Runtime.Dirty = false
		group.Runtime.Pending = 0
		group.Runtime.PendingAtBot = false
		generation := group.Runtime.Generation
		callCtx, cancel := observeContext(group, 90*time.Second)
		group.Runtime.ObserveCancel = cancel
		go func() {
			defer cancel()
			added, err := ag.Observe(callCtx, group.ID, botQQ, group.Prompt, memory, messages)
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

			var ev onebot.Event
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

			text := strings.TrimSpace(onebot.ExtractText(ev))
			if text == "" {
				log.Printf("收到群消息但文本为空：group_id=%d user_id=%d", ev.GroupID, ev.UserID)
				continue
			}
			incoming := incoming.Run(incoming.GroupMessage{Event: ev, Text: text}, incoming.AtBot(botQQ))
			log.Printf("收到群消息：group_id=%d user_id=%d at_bot=%v text=%q", ev.GroupID, ev.UserID, incoming.AtBot, text)
			if strings.HasPrefix(text, atPrefix) {
				switch strings.TrimSpace(text[len(atPrefix):]) {
				case cmdPing:
					writeMu.Lock()
					err := onebot.SendGroupMsg(conn, ev.GroupID, "pong")
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
					if !b.cfg.IsMaintainer(ev.UserID) {
						log.Printf("群 %d 非维护者尝试 /bill：user_id=%d", ev.GroupID, ev.UserID)
						continue
					}
					msg, err := bottools.GroupBillText(ev.GroupID)
					if err != nil {
						msg = fmt.Sprintf("查询账单失败：%v", err)
					}
					writeMu.Lock()
					err = onebot.SendPrivateMsg(conn, ev.UserID, ev.GroupID, msg)
					writeMu.Unlock()
					if err != nil {
						log.Printf("私发账单失败：group_id=%d user_id=%d err=%v", ev.GroupID, ev.UserID, err)
					} else {
						log.Printf("已私发账单：group_id=%d user_id=%d", ev.GroupID, ev.UserID)
					}
					continue
				}
			}

			t := session.EventTime(ev)
			if !group.Runtime.Observing && group.Runtime.Session.ExpiredAt(t) {
				log.Printf("群 %d 当前 session 超过 20 分钟无新消息，触发 final observe", group.ID)
				finalObserve(group)
				group.Runtime = session.Runtime{Generation: group.Runtime.Generation + 1}
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
			next, err := config.Load(configPath)
			if err != nil {
				log.Printf("reload go.toml failed: %v", err)
				continue
			}
			reloadGroups(b.groups, next, stopTimer, finalObserve, func(groupID int64, userID int64) {
				writeMu.Lock()
				defer writeMu.Unlock()
				if err := onebot.PokeGroupMember(conn, groupID, userID); err != nil {
					log.Printf("发送配置变更戳一戳失败：group_id=%d user_id=%d err=%v", groupID, userID, err)
				}
			})
			b.cfg = next
			cfg = next
		}
	}
}

