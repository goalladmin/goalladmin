//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package metrics

import "time"

// processCPUTime 在这些系统上取不到进程 CPU 时间，监控页面不显示 CPU 占用。
func processCPUTime() (time.Duration, bool) { return 0, false }
