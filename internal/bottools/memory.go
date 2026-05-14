package bottools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var memoryMu sync.Mutex

type memoryFileKey struct{}

func WithMemoryFile(ctx context.Context, path string) context.Context {
	return context.WithValue(ctx, memoryFileKey{}, path)
}

func replaceMemory(path, text string) error {
	memoryMu.Lock()
	defer memoryMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(text), 0600)
}

func MemoryPromptSection(path string) string {
	memoryMu.Lock()
	defer memoryMu.Unlock()

	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func ReplaceMemoryPrompt(ctx context.Context, args *struct {
	Text string `description:"新的完整跨 session 记忆 prompt。不是追加一条，而是替换整份记忆文件。你可以删除过时内容、压缩重复内容、融合新旧事实、按重要性重写成未来 session 直接可读的背景信息。"`
}) string {
	text := strings.TrimSpace(args.Text)
	if text == "" {
		return "ReplaceMemoryPrompt 失败：text 为空。"
	}
	path, ok := ctx.Value(memoryFileKey{}).(string)
	if !ok {
		return "ReplaceMemoryPrompt 失败：当前上下文没有记忆文件。"
	}
	if err := replaceMemory(path, text); err != nil {
		return fmt.Sprintf("ReplaceMemoryPrompt 失败：%v", err)
	}
	return "记忆 prompt 已替换。当前 session 的 system prompt 不会因此改变，新记忆会从下一个 session 开始注入。"
}
