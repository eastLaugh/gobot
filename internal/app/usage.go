package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const usageDir = ".run/usage"

type usageRecord struct {
	Time             string  `json:"time"`
	GroupID          int64   `json:"group_id"`
	Round            int     `json:"round"`
	Model            string  `json:"model"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CachedTokens     int64   `json:"cached_tokens"`
	UncachedTokens   int64   `json:"uncached_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	CostYuan         float64 `json:"cost_yuan"`
	SentGroupMessage bool    `json:"sent_group_message"`
}

func uncachedPromptTokens(prompt, cached int64) int64 {
	uncached := prompt - cached
	if uncached < 0 {
		return 0
	}
	return uncached
}

func usageCostYuan(prompt, cached, completion int64) float64 {
	uncached := uncachedPromptTokens(prompt, cached)
	return (float64(uncached)*1 + float64(cached)*0.2 + float64(completion)*2) / 1_000_000
}

func appendUsageRecord(usage tokenUsage) error {
	if err := os.MkdirAll(usageDir, 0700); err != nil {
		return err
	}
	rec := usageRecord{
		Time:             time.Now().Format(time.RFC3339),
		GroupID:          usage.GroupID,
		Round:            usage.Round,
		Model:            usage.Model,
		PromptTokens:     usage.PromptTokens,
		CachedTokens:     usage.CachedTokens,
		UncachedTokens:   uncachedPromptTokens(usage.PromptTokens, usage.CachedTokens),
		CompletionTokens: usage.CompletionTokens,
		TotalTokens:      usage.TotalTokens,
		CostYuan:         usage.CostYuan,
		SentGroupMessage: usage.NotifyPrivate,
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	path := filepath.Join(usageDir, fmt.Sprintf("%d.jsonl", usage.GroupID))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

var (
	usageMu         sync.Mutex
	usageSinceStart float64
)

type observeUsageKey struct{}

func withObserveUsage(ctx context.Context, spent *float64) context.Context {
	return context.WithValue(ctx, observeUsageKey{}, spent)
}

func accumulateUsage(ctx context.Context, usage tokenUsage) tokenUsage {
	cost := usageCostYuan(usage.PromptTokens, usage.CachedTokens, usage.CompletionTokens)
	usage.CostYuan = cost
	if p, ok := ctx.Value(observeUsageKey{}).(*float64); ok && p != nil {
		*p += cost
		usage.ObserveTotal = *p
	} else {
		usage.ObserveTotal = cost
	}
	usageMu.Lock()
	usageSinceStart += cost
	usage.SinceStartTotal = usageSinceStart
	usageMu.Unlock()
	return usage
}

func formatTokenUsage(usage tokenUsage) string {
	uncached := uncachedPromptTokens(usage.PromptTokens, usage.CachedTokens)
	return fmt.Sprintf("本次发言 token 用量（群 %d）：\n输入 %d tokens\n输入（已缓存）%d tokens\n输出 %d tokens\n总计 %d tokens\n本次费用约 %.6f 元\n本 observe 累计约 %.4f 元\n自启动累计约 %.4f 元",
		usage.GroupID, uncached, usage.CachedTokens, usage.CompletionTokens, usage.TotalTokens, usage.CostYuan, usage.ObserveTotal, usage.SinceStartTotal)
}

func logUsageRecord(usage tokenUsage) {
	if err := appendUsageRecord(usage); err != nil {
		log.Printf("写入 token 用量失败：group_id=%d err=%v", usage.GroupID, err)
	}
}
