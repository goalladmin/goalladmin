package app

import (
	"context"
	"time"
)

// startSessionCleanup 只在全部模块启动成功后调用，CLI 的 Setup 不会开始清理（D-081）。
func (a *App) startSessionCleanup(parent context.Context) {
	if a.deps.DB == nil || len(a.authenticators) == 0 || a.sessionCleanupCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(a.Context(parent))
	a.sessionCleanupCancel = cancel
	done := make(chan struct{})
	a.sessionCleanupDone = done
	go func() {
		defer close(done)
		runSessionCleanup(ctx, a.cleanupSessions)
	}()
}

func (a *App) stopSessionCleanup() {
	if a.sessionCleanupCancel != nil {
		a.sessionCleanupCancel()
		<-a.sessionCleanupDone
		a.sessionCleanupCancel = nil
		a.sessionCleanupDone = nil
	}
}

// runSessionCleanup 用完成后的等待时间控制频率，慢查询不会造成任务重叠或补跑。
// clean 返回是否有删除或错误；这两种情况下一分钟继续，其余一小时后再检查。
func runSessionCleanup(ctx context.Context, clean func(context.Context) bool) {
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if ctx.Err() != nil {
				return
			}
			delay := time.Hour
			if clean(ctx) {
				delay = time.Minute
			}
			timer.Reset(delay)
		}
	}
}

func (a *App) cleanupSessions(ctx context.Context) bool {
	busy := false
	for _, p := range a.deps.Portals.All() {
		if ctx.Err() != nil {
			break
		}
		an := a.authenticators[p.Code]
		batchCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		n, err := an.Sessions().Cleanup(batchCtx, p.Code, 30*24*time.Hour)
		cancel()
		if n > 0 {
			busy = true
			a.deps.Log.Info("session history cleaned", "portal", p.Code, "deleted", n)
		}
		if err != nil {
			busy = true
			if ctx.Err() == nil {
				a.deps.Log.Warn("session history cleanup failed", "portal", p.Code, "error", err)
			}
		}
	}
	return busy
}
