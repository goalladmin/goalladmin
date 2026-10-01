package auditimpl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCleanMessage(t *testing.T) {
	in := `Error 1062 (23000): Duplicate entry 'alice@example.com' for key "uk_user" password=hunter2, token: abc.def dial tcp ga:s3cret@tcp(10.0.0.1:3306)`
	out := cleanMessage(in)
	require.NotContains(t, out, "alice@example.com")
	require.NotContains(t, out, "hunter2")
	require.NotContains(t, out, "abc.def")
	require.NotContains(t, out, "s3cret")
	require.Contains(t, out, "Duplicate entry ? for key ?")
	require.Contains(t, out, "password=***")
	require.Contains(t, out, "token: ***")

	long := strings.Repeat("错", 2000)
	require.LessOrEqual(t, len(cleanMessage(long)), maxMessage)

	// 指纹只看"同一类"：数字和长十六进制串不同，指纹相同
	a := normalizeForFingerprint(cleanMessage("dial tcp 10.0.0.1:3306: i/o timeout after 1500ms id=deadbeef01"))
	b := normalizeForFingerprint(cleanMessage("dial tcp 10.0.0.2:3307: i/o timeout after 2000ms id=cafebabe99"))
	require.Equal(t, a, b)
	require.Equal(t, fingerprint("x", a), fingerprint("x", b))
	require.Len(t, fingerprint("x"), 32)
}

func TestCleanStack(t *testing.T) {
	raw := "goroutine 7 [running]:\n" +
		"runtime/debug.Stack()\n\t/usr/local/go/src/runtime/debug/stack.go:26 +0x5e\n" +
		"github.com/acme/app/server/core/internal/middleware.Recovery.func1.1()\n\t/home/builder/work/app/server/core/internal/middleware/middleware.go:56 +0x3c\n" +
		"panic({0x1234, 0xc000})\n\t/usr/local/go/src/runtime/panic.go:787 +0x132\n" +
		"runtime.panicmem(...)\n\t/usr/local/go/src/runtime/panic.go:262\n" +
		"runtime.sigpanic()\n\t/usr/local/go/src/runtime/signal_unix.go:925 +0x359\n" +
		"github.com/acme/app/server/modules/demo.(*handlers).boom(0xc0001, 0xc0002)\n\t/home/builder/work/app/server/modules/demo/handlers.go:42 +0x1f\n"
	stack, origin := cleanStack([]byte(raw))
	require.NotContains(t, stack, "goroutine 7")
	require.NotContains(t, stack, "/home/builder")
	require.NotContains(t, stack, "+0x")
	require.NotContains(t, stack, "0xc0001")
	require.Contains(t, stack, "github.com/acme/app/server/modules/demo.(*handlers).boom\n\tmodules/demo/handlers.go:42\n")
	require.Equal(t, "github.com/acme/app/server/modules/demo.(*handlers).boom modules/demo/handlers.go:42", origin)
}

type recorded struct {
	mu   sync.Mutex
	rows map[string]int64
	n    int
}

func (r *recorded) write(_ context.Context, row string, n int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows[row] += n
	r.n++
	return nil
}

func TestThrottle_MergesAndBoundsWrites(t *testing.T) {
	now := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	rec := &recorded{rows: map[string]int64{}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	th := newThrottle("test", clock, log, time.Second, 3, 2, 2, rec.write)
	ctx := context.Background()

	// 同一秒里 500 次：只写第一次
	for range 500 {
		th.Add(ctx, "a", "a", false)
	}
	require.Equal(t, 1, rec.n)
	require.EqualValues(t, 1, rec.rows["a"])

	// 一秒后再来一次：把攒着的 499 次和这一次一起写
	now = now.Add(time.Second)
	th.Add(ctx, "a", "a", false)
	require.Equal(t, 2, rec.n)
	require.EqualValues(t, 501, rec.rows["a"])

	// 新键的写入速度有上限（每秒 2 个，桶容量 2）：b、c 能写，d 被丢弃
	th.Add(ctx, "b", "b", false)
	th.Add(ctx, "c", "c", false)
	th.Add(ctx, "d", "d", false)
	require.EqualValues(t, 1, rec.rows["b"])
	require.EqualValues(t, 1, rec.rows["c"])
	require.EqualValues(t, 0, rec.rows["d"])

	// Flush 写完所有攒着的次数
	th.Add(ctx, "b", "b", false)
	th.Add(ctx, "b", "b", false)
	th.Flush(ctx)
	require.EqualValues(t, 3, rec.rows["b"])

	// 键数到上限时淘汰没有攒着次数的键，再出现的按新键写（库里是累加）
	now = now.Add(10 * time.Second)
	th.Add(ctx, "d", "d", false)
	require.EqualValues(t, 1, rec.rows["d"])
	require.LessOrEqual(t, len(th.entries), 3)
}

func TestThrottle_FlushDue(t *testing.T) {
	now := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	rec := &recorded{rows: map[string]int64{}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	th := newThrottle("test", clock, log, time.Second, 100, 100, 100, rec.write)
	ctx := context.Background()
	for range 5 {
		th.Add(ctx, "a", "a", false)
	}
	th.FlushDue(ctx)
	require.EqualValues(t, 1, rec.rows["a"], "不到一秒不写")
	now = now.Add(time.Second)
	th.FlushDue(ctx)
	require.EqualValues(t, 5, rec.rows["a"], "没有新记录时，后台定时把攒着的次数写掉")
}

type flaky struct {
	recorded
	fail bool
}

func (f *flaky) write(ctx context.Context, row string, n int64) error {
	if f.fail {
		return context.DeadlineExceeded
	}
	return f.recorded.write(ctx, row, n)
}

// 写库失败的次数放回去，数据库恢复后一次不少地写进去。
func TestThrottle_FailedWritesAreRetried(t *testing.T) {
	now := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	f := &flaky{recorded: recorded{rows: map[string]int64{}}, fail: true}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	th := newThrottle("test", clock, log, time.Second, 100, 100, 100, f.write)
	ctx := context.Background()
	for range 10 {
		th.Add(ctx, "a", "a", false)
	}
	now = now.Add(time.Second)
	th.FlushDue(ctx) // 仍然失败
	f.fail = false
	now = now.Add(time.Second)
	th.FlushDue(ctx)
	require.EqualValues(t, 10, f.rows["a"])
}

// 优先级高的新键不受写入速度限制：攻击者刷新键把桶耗光时，严重事件照样写进去。
func TestThrottle_PriorityBypassesNewKeyLimit(t *testing.T) {
	now := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	rec := &recorded{rows: map[string]int64{}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	th := newThrottle("test", clock, log, time.Second, 3, 1, 1, rec.write)
	ctx := context.Background()
	th.Add(ctx, "noise-1", "noise-1", false)
	th.Add(ctx, "noise-2", "noise-2", false) // 桶空了，丢弃
	require.EqualValues(t, 0, rec.rows["noise-2"])
	th.Add(ctx, "critical", "critical", true)
	require.EqualValues(t, 1, rec.rows["critical"])
	// 键满了、又没有可淘汰的（都攒着次数）时，优先级高的直接写
	th.Add(ctx, "noise-1", "noise-1", false)
	th.Add(ctx, "critical", "critical", false)
	th.Add(ctx, "x", "x", true)
	th.Add(ctx, "y", "y", true)
	require.EqualValues(t, 1, rec.rows["y"])
}

// 数据库一直写不进去时，关停不会被拖住：Flush 有总时限，后台一轮被 ctx 打断后立刻停下。
func TestThrottle_FlushGivesUpWhenCancelled(t *testing.T) {
	now := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	calls := 0
	slow := func(ctx context.Context, _ string, _ int64) error {
		calls++
		<-ctx.Done()
		return ctx.Err()
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	th := newThrottle("test", clock, log, time.Second, 100, 1000, 1000, slow)
	for i := range 50 {
		th.entries[fmt.Sprint(i)] = &entry[string]{row: "x", pending: 1}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	th.FlushDue(ctx)
	th.Flush(ctx)
	require.Less(t, time.Since(start), time.Second)
	require.LessOrEqual(t, calls, 2)
	var left int64
	for _, e := range th.entries {
		left += e.pending
	}
	require.EqualValues(t, 50, left, "没写进去的次数还在")
}

// 规范 §13.2 第 104 条（D-058）：这一行数据本身写不进去（permanent）时不放回去重试，键不会一直攒着次数、
// 占着位置；表满以后别的记录照样能进来。数据库暂时连不上这类可以重试的错误照旧放回去。
func TestThrottle_104_PermanentFailuresAreDropped(t *testing.T) {
	now := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	rec := &recorded{rows: map[string]int64{}}
	bad := errors.New("row cannot be stored")
	write := func(ctx context.Context, row string, n int64) error {
		if strings.HasPrefix(row, "bad") {
			return bad
		}
		return rec.write(ctx, row, n)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	th := newThrottle("test", clock, log, time.Second, 3, 100, 100, write)
	th.permanent = func(err error) bool { return errors.Is(err, bad) }
	ctx := context.Background()
	for i := range 3 {
		th.Add(ctx, fmt.Sprintf("bad-%d", i), fmt.Sprintf("bad-%d", i), false)
		th.Add(ctx, fmt.Sprintf("bad-%d", i), fmt.Sprintf("bad-%d", i), false)
	}
	now = now.Add(time.Second)
	th.FlushDue(ctx)
	for k, e := range th.entries {
		require.Zero(t, e.pending, "%s：写不进去的次数不放回去", k)
	}
	th.Add(ctx, "good", "good", false)
	require.EqualValues(t, 1, rec.rows["good"], "表满时能淘汰写不进去的键，新记录照样写进去")
}
