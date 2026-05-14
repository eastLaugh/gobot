package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

var memoryMu sync.Mutex

func appendMemory(text string) error {
	memoryMu.Lock()
	defer memoryMu.Unlock()

	f, err := os.OpenFile("memory.prompt", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = fmt.Fprintf(f, "[%s] %s\n", time.Now().Format("2006-01-02"), text)
	return err
}

func MemoryPromptSection() string {
	memoryMu.Lock()
	defer memoryMu.Unlock()

	b, err := os.ReadFile("memory.prompt")
	if err != nil {
		return ""
	}
	return string(b)
}

func Remember(ctx context.Context, args *struct {
	Text string `description:"要追加到跨 session 长期记忆的一条事实。写成以后可以直接阅读的事实本身，不要写「用户让我记住」这类过程。"`
}) string {
	text := strings.TrimSpace(args.Text)
	if text == "" {
		return "Remember 失败：text 为空。"
	}
	if err := appendMemory(text); err != nil {
		return fmt.Sprintf("Remember 失败：%v", err)
	}
	return "已记住。"
}
