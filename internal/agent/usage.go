package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

const usageDir = ".run/usage"

type TokenUsage struct {
	GroupID          int64
	Round            int
	Model            string
	PromptTokens     int64
	CachedTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	CostYuan         float64
}

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

func appendUsageRecord(usage TokenUsage) error {
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

type ObserveUsageAcc struct {
	GroupID          int64
	CostYuan         float64
	PromptTokens     int64
	CachedTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}

type observeUsageKey struct{}

func WithObserveUsage(ctx context.Context, acc *ObserveUsageAcc) context.Context {
	return context.WithValue(ctx, observeUsageKey{}, acc)
}

type tokenUsageReporterKey struct{}

func WithTokenUsageReporter(ctx context.Context, report func(TokenUsage)) context.Context {
	return context.WithValue(ctx, tokenUsageReporterKey{}, report)
}

func reportTokenUsage(ctx context.Context, usage TokenUsage) {
	report, ok := ctx.Value(tokenUsageReporterKey{}).(func(TokenUsage))
	if ok {
		report(usage)
	}
}

func AccumulateUsage(ctx context.Context, usage TokenUsage) TokenUsage {
	cost := usageCostYuan(usage.PromptTokens, usage.CachedTokens, usage.CompletionTokens)
	usage.CostYuan = cost
	if acc, ok := ctx.Value(observeUsageKey{}).(*ObserveUsageAcc); ok && acc != nil {
		acc.CostYuan += cost
		acc.PromptTokens += usage.PromptTokens
		acc.CachedTokens += usage.CachedTokens
		acc.CompletionTokens += usage.CompletionTokens
		acc.TotalTokens += usage.TotalTokens
	}
	return usage
}

func LogUsageRecord(usage TokenUsage) {
	if err := appendUsageRecord(usage); err != nil {
		log.Printf("写入 token 用量失败：group_id=%d err=%v", usage.GroupID, err)
	}
}
