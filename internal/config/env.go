package config

import (
	"cmp"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

func LoadDotEnv(path string) error {
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

func Ensure(path string, example []byte) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.WriteFile(path, example, 0600); err != nil {
		return err
	}
	log.Printf("%s 不存在，已从内置模板创建。请编辑后保存关闭。", path)
	cmd := editorCommand(path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func editorCommand(path string) *exec.Cmd {
	editor := cmp.Or(os.Getenv("GOBOT_EDITOR"), os.Getenv("VISUAL"), os.Getenv("EDITOR"))
	if editor != "" {
		args := strings.Fields(editor)
		return exec.Command(args[0], append(args[1:], path)...)
	}
	if runtime.GOOS == "darwin" {
		return exec.Command("open", "-W", "-t", path)
	}
	return exec.Command("vi", path)
}
