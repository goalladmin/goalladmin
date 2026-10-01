// 开发用的自动重启：监视 server/ 下的源码，一有改动就重新编译、重启服务；前端的 Vite 开发服务器本来就会热更新。
// 只在开发机上用（make dev），不进服务二进制；用轮询而不是文件系统事件，不引入第三方依赖。
//
//	go run ./cmd/dev [传给服务的参数，如 serve -config config/config.yaml]
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	pollInterval = 500 * time.Millisecond
	settle       = 300 * time.Millisecond // 改动停下来这么久再编译，一次保存多个文件只编一次
	stopTimeout  = 10 * time.Second
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		args = []string{"serve"}
	}
	bin := filepath.Join(os.TempDir(), fmt.Sprintf("goalladmin-dev-%d", os.Getpid()))
	defer func() { _ = os.Remove(bin) }()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	// 子进程由 stop 优雅结束，不靠 ctx 取消；这里的 ctx 只是为了用 CommandContext
	ctx := context.Background()
	var child *exec.Cmd
	last := snapshot()
	// 先编译到临时文件，成功了才停掉旧服务、换上新的：改到一半编译不过时，旧服务照常可用
	restart := func() {
		say("编译中……")
		next := bin + ".next"
		build := exec.CommandContext(ctx, "go", "build", "-o", next, ".") //nolint:gosec // 开发工具：编译当前目录
		build.Stdout, build.Stderr = os.Stdout, os.Stderr
		if err := build.Run(); err != nil {
			_ = os.Remove(next)
			if child != nil {
				say("编译失败，旧服务继续运行；改好后会自动重试")
			} else {
				say("编译失败，改好后会自动重试")
			}
			return
		}
		stop(child)
		child = nil
		if err := os.Rename(next, bin); err != nil {
			say("替换程序失败: %v", err)
			return
		}
		child = exec.CommandContext(ctx, bin, args...) //nolint:gosec // 开发工具：运行刚编译出来的服务
		child.Stdout, child.Stderr, child.Stdin = os.Stdout, os.Stderr, os.Stdin
		if err := child.Start(); err != nil {
			say("启动失败: %v", err)
			child = nil
			return
		}
		say("服务已启动（pid %d），监视源码改动中", child.Process.Pid)
	}
	restart()
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()
	for {
		select {
		case <-sig:
			stop(child)
			return
		case <-tick.C:
			cur := snapshot()
			if cur == last {
				continue
			}
			// 等改动停下来
			for {
				time.Sleep(settle)
				next := snapshot()
				if next == cur {
					break
				}
				cur = next
			}
			last = cur
			say("源码有改动，重启")
			restart()
		}
	}
}

// snapshot 把要监视的文件的路径、大小、修改时间拼成一个串：任何一个变了，串就不同。
func snapshot() string {
	var b strings.Builder
	_ = filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // 读不了的目录跳过，不影响监视别的文件
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "bin", "tmp", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(p) {
		case ".go", ".sql", ".json", ".lock", ".yaml", ".yml", ".mod", ".sum":
		default:
			return nil
		}
		if strings.HasSuffix(p, "_test.go") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil //nolint:nilerr // 文件刚被删掉：下一轮再看
		}
		fmt.Fprintf(&b, "%s|%d|%d\n", p, info.Size(), info.ModTime().UnixNano())
		return nil
	})
	return b.String()
}

// stop 先让服务优雅退出（处理完手上的请求），到时还没退就强制结束。
func stop(c *exec.Cmd) {
	if c == nil || c.Process == nil {
		return
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	if err := c.Process.Signal(os.Interrupt); err != nil && !errors.Is(err, os.ErrProcessDone) {
		_ = c.Process.Kill()
	}
	select {
	case <-done:
	case <-time.After(stopTimeout):
		_ = c.Process.Kill()
		<-done
	}
}

func say(format string, a ...any) { fmt.Fprintf(os.Stderr, "[dev] "+format+"\n", a...) }
