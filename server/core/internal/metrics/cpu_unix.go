//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package metrics

import (
	"syscall"
	"time"
)

// processCPUTime 返回进程累计使用的 CPU 时间（用户态 + 内核态）。
func processCPUTime() (time.Duration, bool) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0, false
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano()), true
}
