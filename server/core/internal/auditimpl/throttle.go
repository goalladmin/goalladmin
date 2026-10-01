package auditimpl

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// throttle 把同一个键的多次记录合并成有限次写库（D-032）：
//   - 键第一次出现立即写一次（次数 1），出事时后台马上能看到；
//   - 之后同一个键每 interval 最多由请求写一次，中间的次数攒着；后台每 interval 调一次 FlushDue 把攒着的写掉，
//     关停前 Flush 写完；写失败的次数放回去，下次再写，不会丢；
//   - 新键的写入速度受 newKeys 限制（优先级高的记录不受限制，攻击者刷新键也挤不掉它们）；
//     键的总数不超过 maxKeys，满了淘汰最久没写、也没有攒着次数的键；写不进的只记日志输出（调用方另有一份）；
//   - 这一行数据本身写不进去（permanent 判定，如字符集、超长）的不放回去：重试多少次都一样，放回去只会让键
//     永远攒着次数、淘汰不掉，占满之后别的记录也进不来（D-058）。
//
// 写库用"插入或累加"（upsert），所以一个键被淘汰后再出现，累加到的仍是库里同一行。
type throttle[R any] struct {
	now      func() time.Time
	interval time.Duration
	maxKeys  int
	newKeys  *bucket
	write    func(ctx context.Context, row R, n int64) error
	// permanent 报告写库错误是不是"这一行本身写不进去"；nil 表示都按可以重试处理
	permanent func(error) bool
	log       *slog.Logger
	what      string // 日志里的名字

	mu      sync.Mutex
	entries map[string]*entry[R]
}

type entry[R any] struct {
	row       R
	pending   int64
	lastWrite time.Time
}

type job[R any] struct {
	key string
	row R
	n   int64
}

// 写库的时限：数据库出问题时不让请求一直卡着，后台一轮、关停时的最后一轮也都有总时限。
const (
	writeTimeout    = 2 * time.Second
	flushDueBudget  = 5 * time.Second
	finalFlushLimit = 10 * time.Second
)

func newThrottle[R any](what string, now func() time.Time, log *slog.Logger, interval time.Duration, maxKeys int, newPerSec float64, burst int,
	write func(ctx context.Context, row R, n int64) error,
) *throttle[R] {
	return &throttle[R]{
		now: now, interval: interval, maxKeys: maxKeys, newKeys: &bucket{rate: newPerSec, burst: float64(burst), tokens: float64(burst)},
		write: write, log: log, what: what, entries: map[string]*entry[R]{},
	}
}

// Add 记一次。row 是这一次的内容（"最近一次"的字段以最后一次为准）。priority 为 true 的新键不受写入速度限制。
func (t *throttle[R]) Add(ctx context.Context, key string, row R, priority bool) {
	now := t.now()
	var jobs []job[R]
	t.mu.Lock()
	e := t.entries[key]
	switch {
	case e != nil:
		e.row = row
		e.pending++
		if now.Sub(e.lastWrite) >= t.interval {
			jobs = append(jobs, job[R]{key: key, row: e.row, n: e.pending})
			e.pending, e.lastWrite = 0, now
		}
	case !t.newKeys.allow(now) && !priority:
		t.mu.Unlock()
		t.log.WarnContext(ctx, t.what+" not persisted: too many new entries", "key", key)
		return
	case len(t.entries) >= t.maxKeys && !t.evictLocked():
		// 满了又腾不出位置：优先级高的直接写一次、不跟踪；其余只进日志输出
		t.mu.Unlock()
		if priority {
			t.run(context.WithoutCancel(ctx), []job[R]{{row: row, n: 1}})
			return
		}
		t.log.WarnContext(ctx, t.what+" not persisted: too many distinct entries", "key", key)
		return
	default:
		t.entries[key] = &entry[R]{row: row, lastWrite: now}
		jobs = append(jobs, job[R]{key: key, row: row, n: 1})
	}
	t.mu.Unlock()
	// 客户端断开会取消请求 ctx；记录仍要写，所以去掉取消信号但保留 ctx 里的值。
	t.run(context.WithoutCancel(ctx), jobs)
}

// FlushDue 把攒了至少 interval 的次数写掉（后台定时调用：没有新记录时计数也不会一直停在旧值）。
// ctx 取消（关停）或本轮用完时限时停下，没写的次数放回去。
func (t *throttle[R]) FlushDue(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, flushDueBudget)
	defer cancel()
	t.mu.Lock()
	jobs := t.dueLocked(t.now(), false)
	t.mu.Unlock()
	t.run(ctx, jobs)
}

// Flush 把所有攒着的次数写掉（关停前、测试里调用）。最多用 finalFlushLimit，到时还没写进去的记一行日志后放弃
// （它们在日志输出里都有），不让数据库故障拖住关停。
func (t *throttle[R]) Flush(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, finalFlushLimit)
	defer cancel()
	t.mu.Lock()
	jobs := t.dueLocked(t.now(), true)
	t.mu.Unlock()
	t.run(ctx, jobs)
	t.mu.Lock()
	var left int64
	for _, e := range t.entries {
		left += e.pending
	}
	t.mu.Unlock()
	if left > 0 {
		t.log.WarnContext(ctx, t.what+" counts not persisted before shutdown", "count", left)
	}
}

// dueLocked 取出到期（或 all 时全部）有次数的键。
func (t *throttle[R]) dueLocked(now time.Time, all bool) []job[R] {
	var jobs []job[R]
	for k, e := range t.entries {
		if e.pending > 0 && (all || now.Sub(e.lastWrite) >= t.interval) {
			jobs = append(jobs, job[R]{key: k, row: e.row, n: e.pending})
			e.pending, e.lastWrite = 0, now
		}
	}
	return jobs
}

// evictLocked 淘汰一个没有攒着次数、最久没写过的键，腾出位置；没有可淘汰的返回 false。
// 被淘汰的键再出现时按新键处理，写库时累加到同一行。
func (t *throttle[R]) evictLocked() bool {
	victim, oldest := "", time.Time{}
	for k, e := range t.entries {
		if e.pending == 0 && (victim == "" || e.lastWrite.Before(oldest)) {
			victim, oldest = k, e.lastWrite
		}
	}
	if victim == "" {
		return false
	}
	delete(t.entries, victim)
	return true
}

// run 逐个写库，每次写各有时限。写失败的次数放回对应的键，由后台下次再写；ctx 结束后不再写，剩下的也放回去。
func (t *throttle[R]) run(ctx context.Context, jobs []job[R]) {
	failed := 0
	for i, j := range jobs {
		if ctx.Err() != nil {
			for _, rest := range jobs[i:] {
				t.requeue(ctx, rest)
			}
			break
		}
		wctx, cancel := context.WithTimeout(ctx, writeTimeout)
		err := t.write(wctx, j.row, j.n)
		cancel()
		if err != nil {
			if t.permanent != nil && t.permanent(err) {
				// 放弃这一批次数：这一行写不进去（日志输出里有它）
				t.log.ErrorContext(ctx, t.what+" dropped: row cannot be stored", "err", err, "count", j.n)
				continue
			}
			failed++
			if failed == 1 {
				t.log.ErrorContext(ctx, t.what+" write failed, will retry", "err", err, "count", j.n)
			}
			t.requeue(ctx, j)
		}
	}
}

// requeue 把没写进去的次数放回去。
func (t *throttle[R]) requeue(ctx context.Context, j job[R]) {
	if j.key == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if e := t.entries[j.key]; e != nil {
		e.pending += j.n
		return
	}
	if len(t.entries) >= t.maxKeys && !t.evictLocked() {
		t.log.WarnContext(ctx, t.what+" dropped after write failure", "count", j.n)
		return
	}
	t.entries[j.key] = &entry[R]{row: j.row, pending: j.n}
}

// bucket 是令牌桶：每秒补 rate 个，最多存 burst 个。调用方持有 throttle 的锁。
type bucket struct {
	rate, burst, tokens float64
	last                time.Time
}

func (b *bucket) allow(now time.Time) bool {
	if !b.last.IsZero() {
		b.tokens += now.Sub(b.last).Seconds() * b.rate
		if b.tokens > b.burst {
			b.tokens = b.burst
		}
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
