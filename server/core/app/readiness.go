package app

import (
	"context"
	"sync"
	"time"
)

const (
	readinessTTL     = time.Second
	readinessTimeout = 2 * time.Second
)

// readinessCheck 只缓存数据库检查结果，响应由每个请求自行生成（D-084）。
// 共用一个完成信号，不为等待者启动额外协程或保留回调。
type readinessCheck struct {
	now  func() time.Time
	ping func(context.Context) error

	mu     sync.Mutex
	until  time.Time
	err    error
	active *readinessCall
	closed bool
}

type readinessCall struct {
	done   chan struct{}
	cancel context.CancelFunc
	err    error // 关闭 done 后只读
}

func (r *readinessCheck) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return context.Canceled
	}
	if r.now().Before(r.until) {
		err := r.err
		r.mu.Unlock()
		return err
	}
	call := r.active
	if call == nil {
		// 共享检查不绑定第一个 HTTP 请求，单个探针取消不能影响其他探针。
		probeCtx, cancel := context.WithTimeout(context.Background(), readinessTimeout)
		call = &readinessCall{done: make(chan struct{}), cancel: cancel}
		r.active = call
		go r.run(probeCtx, call)
	}
	r.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-call.done:
		if err := ctx.Err(); err != nil {
			return err
		}
		return call.err
	}
}

func (r *readinessCheck) run(ctx context.Context, call *readinessCall) {
	err := r.ping(ctx)
	if err == nil {
		err = ctx.Err()
	}
	call.cancel()
	r.mu.Lock()
	defer r.mu.Unlock()
	call.err = err
	r.err, r.until = err, r.now().Add(readinessTTL)
	r.active = nil
	close(call.done)
}

func (r *readinessCheck) stop() {
	r.mu.Lock()
	r.closed = true
	call := r.active
	r.mu.Unlock()
	if call != nil {
		call.cancel()
		<-call.done
	}
}
