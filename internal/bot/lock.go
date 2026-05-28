package bot

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

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
