package redisx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// eventually 等 cond 成立，最多 3 秒。
func eventually(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("等不到：%s", msg)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func set(c *Client, key, val string) error {
	return c.Do(context.Background(), func(ctx context.Context, rdb redis.Cmdable) error {
		return rdb.Set(ctx, c.Key(key), val, time.Minute).Err()
	})
}

func get(c *Client, key string) (string, error) {
	var v string
	err := c.Do(context.Background(), func(ctx context.Context, rdb redis.Cmdable) error {
		var err error
		v, err = rdb.Get(ctx, c.Key(key)).Result()
		return err
	})
	return v, err
}

// openFake 连一个假服务器（探测间隔调短）。
func openFake(t *testing.T, f *TestFakeServer, o Options) (*Client, error) {
	t.Helper()
	o.Addr, o.KeyPrefix, o.ProbeEvery = f.Addr(), "t:", 10*time.Millisecond
	if o.Log == nil {
		o.Log = quiet()
	}
	c, err := Open(o)
	if c != nil {
		t.Cleanup(func() { _ = c.Close() })
	}
	return c, err
}

// 规范 §13.2 第 163 条（D-074）：连上真的 Redis——可用、读得到版本、键带前缀；读写正常；
// 键不存在（redis.Nil）、服务器回的普通错误（类型不对）、调用方自己的错误原样返回，不把 Redis 标成不可用。
func TestOpen_163_ConnectsReadsVersionAndRuns(t *testing.T) {
	c := OpenTest(t, nil)
	require.True(t, c.Available())
	require.Regexp(t, `^\d+\.\d+`, c.Version())
	major, _, _ := strings.Cut(c.Version(), ".")
	n, err := strconv.Atoi(major)
	require.NoError(t, err)
	require.GreaterOrEqual(t, n, MinMajor)

	require.True(t, strings.HasPrefix(c.Key("a", "b"), "gatest:"))
	require.True(t, strings.HasSuffix(c.Key("a", "b"), ":a:b"))
	require.NoError(t, set(c, "k", "v"))
	v, err := get(c, "k")
	require.NoError(t, err)
	require.Equal(t, "v", v)

	_, err = get(c, "missing")
	require.ErrorIs(t, err, redis.Nil)
	require.NotErrorIs(t, err, ErrUnavailable)
	require.True(t, c.Available(), "键不存在不是故障")

	// 服务器回的普通错误：对字符串做列表操作
	err = c.Do(context.Background(), func(ctx context.Context, rdb redis.Cmdable) error {
		return rdb.LPush(ctx, c.Key("k"), "x").Err()
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "WRONGTYPE")
	require.NotErrorIs(t, err, ErrUnavailable)
	require.True(t, c.Available(), "服务器能回错误，说明连接是通的")

	// 调用方自己的错误（比如把回复解析成数字失败）
	mine := errors.New("caller's own error")
	err = c.Do(context.Background(), func(context.Context, redis.Cmdable) error { return mine })
	require.ErrorIs(t, err, mine)
	require.NotErrorIs(t, err, ErrUnavailable)
	require.True(t, c.Available())

	v, err = get(c, "k")
	require.NoError(t, err)
	require.Equal(t, "v", v)
}

// 规范 §13.2 第 163 条（D-074）：服务器版本低于 6.0 拒绝启动（返回 TooOldError，说明版本和最低要求）；
// 6.0 起都接受；版本读不出来（格式不认识、接入层不让查、这个账号没有查的权限）照常用。
// 握手用旧协议（RESP2），不发客户端标识、维护通知之类的可选命令；可用的时候后台不发任何命令。
func TestOpen_163_ServerVersion(t *testing.T) {
	for _, old := range []string{"5.0.14", "4.0.9", "2.8.24", "0.0.1"} {
		c, err := openFake(t, NewTestFakeServer(t, "# Server\r\nredis_version:"+old+"\r\nredis_mode:standalone\r\n"), Options{})
		var tooOld *TooOldError
		require.ErrorAs(t, err, &tooOld, old)
		require.Nil(t, c)
		require.Equal(t, old, tooOld.Version)
		require.Contains(t, err.Error(), old)
		require.Contains(t, err.Error(), "6.0")
	}
	for _, ok := range []string{"6.0.0", "6.2.14", "7.0.15", "7.4.11", "8.10.2", "10.0.0", "255.255.255"} {
		f := NewTestFakeServer(t, "# Server\r\nredis_version:"+ok+"\r\n")
		c, err := openFake(t, f, Options{})
		require.NoError(t, err, ok)
		require.True(t, c.Available(), ok)
		require.Equal(t, ok, c.Version())
		// 兼容老服务器和代理接入：握手只有 HELLO 2，探测只用基础命令及发布订阅（D-075）
		for _, cmd := range f.Commands() {
			require.Contains(t, []string{"HELLO", "PING", "INFO", "SET", "SUBSCRIBE", "PUBLISH"}, cmd, "握手和探测只该有这几条命令")
		}
		require.Contains(t, f.Lines(), "HELLO 2", "用 RESP2")
		// 可用的时候后台不发任何命令（探测只在不可用时做）
		n := len(f.Commands())
		time.Sleep(80 * time.Millisecond)
		require.Len(t, f.Commands(), n, ok)
	}
	// 读不出版本：照常用，版本是空的
	for name, info := range map[string]string{
		"没有版本这一行": "# Server\r\nredis_mode:standalone\r\n",
		"版本不是数字":  "redis_version:abc\r\n",
		"空的":      "",
		"没有权限":    "-NOPERM this user has no permissions to run the 'info' command",
		"不认识的命令":  "-ERR unknown command 'INFO'",
	} {
		c, err := openFake(t, NewTestFakeServer(t, info), Options{})
		require.NoError(t, err, name)
		require.True(t, c.Available(), name)
		require.Equal(t, "", c.Version(), name)
	}
}

// 规范 §13.2 第 163 条（D-074）：服务器明确拒绝这份配置（要密码没给、密码或用户名不对、没有权限、库号不存在、
// 这个账号写不了）是配置错误，拒绝启动，错误里带服务器的说法、不带密码；服务器暂时用不了（正在加载、只读、内存满）
// 不是配置错误，照常启动、按不可用处理，好了自动恢复。
func TestOpen_163_RejectedConfigVersusTemporaryTrouble(t *testing.T) {
	const info = "redis_version:7.2.4\r\n"
	for name, tc := range map[string]struct{ cmd, reply string }{
		"要密码没给":   {"PING", "-NOAUTH Authentication required."},
		"密码不对":    {"PING", "-WRONGPASS invalid username-password pair or user is disabled."},
		"没有连接权限":  {"PING", "-NOPERM this user has no permissions to run the 'ping' command"},
		"库号不存在":   {"SELECT", "-ERR DB index is out of range"},
		"没设密码却发了": {"AUTH", "-ERR AUTH <password> called without any password configured for the default user. Are you sure your configuration is correct?"},
		"账号写不了":   {"SET", "-NOPERM this user has no permissions to access one of the keys used as arguments"},
	} {
		f := NewTestFakeServer(t, info)
		f.SetReply(tc.cmd, tc.reply)
		c, err := openFake(t, f, Options{Password: "s3cret-redis-pass", DB: 3, ConnectWait: time.Second})
		var rejected *RejectedError
		require.ErrorAs(t, err, &rejected, name)
		require.Nil(t, c, name)
		require.Contains(t, err.Error(), strings.TrimPrefix(tc.reply, "-")[:10], name)
		require.Contains(t, err.Error(), "redis.password", "提示该检查哪些配置")
		require.NotContains(t, err.Error(), "s3cret-redis-pass", name)
	}
	for name, tc := range map[string]struct{ cmd, reply string }{
		"正在加载数据": {"PING", "-LOADING Redis is loading the dataset in memory"},
		"只读的副本":  {"SET", "-READONLY You can't write against a read only replica."},
		"内存满了":   {"SET", "-OOM command not allowed when used memory > 'maxmemory'."},
		"落盘出错":   {"SET", "-MISCONF Redis is configured to save RDB snapshots, but it's currently unable to persist to disk."},
	} {
		f := NewTestFakeServer(t, info)
		f.SetReply(tc.cmd, tc.reply)
		c, err := openFake(t, f, Options{})
		require.NoError(t, err, name)
		require.False(t, c.Available(), name)
		f.SetReply(tc.cmd, "")
		eventually(t, c.Available, name+"：好了之后恢复")
	}
}

// 查版本的时候连接断了（网络错误，不是"不让查"）：这次探测不算通，不能当成"版本读不出来、照常用"。
func TestOpen_163_VersionReadNetworkErrorIsNotOK(t *testing.T) {
	f := NewTestFakeServer(t, "!drop")
	c, err := openFake(t, f, Options{})
	require.NoError(t, err)
	require.False(t, c.Available())
	time.Sleep(80 * time.Millisecond)
	require.False(t, c.Available())
	f.SetInfo("redis_version:7.2.4\r\n")
	eventually(t, c.Available, "查得到版本之后恢复")
}

// 规范 §13.2 第 163 条（D-074）：用户名、密码、库号、TLS 都按配置用上。没设密码不发 AUTH；库号 0 不发 SELECT；
// 开了 TLS 就不会用明文去连（连明文的服务器连不上）。
func TestOpen_163_CredentialsDBAndTLS(t *testing.T) {
	has := func(f *TestFakeServer, prefix string) bool {
		for _, l := range f.Lines() {
			if strings.HasPrefix(l, prefix) {
				return true
			}
		}
		return false
	}
	const info = "redis_version:7.2.4\r\n"
	f := NewTestFakeServer(t, info)
	c, err := openFake(t, f, Options{})
	require.NoError(t, err)
	require.True(t, c.Available())
	require.False(t, has(f, "AUTH"), f.Lines())
	require.False(t, has(f, "SELECT"), f.Lines())

	f = NewTestFakeServer(t, info)
	c, err = openFake(t, f, Options{Password: "only-pass"})
	require.NoError(t, err)
	require.True(t, c.Available())
	require.Contains(t, f.Lines(), "AUTH only-pass")

	f = NewTestFakeServer(t, info)
	c, err = openFake(t, f, Options{Username: "app", Password: "pw", DB: 3})
	require.NoError(t, err)
	require.True(t, c.Available())
	require.Contains(t, f.Lines(), "AUTH app pw")
	require.Contains(t, f.Lines(), "SELECT 3")

	// TLS：对着明文的服务器握手不成，按不可用处理（没有悄悄退回明文）
	f = NewTestFakeServer(t, info)
	c, err = openFake(t, f, Options{TLS: true})
	require.NoError(t, err)
	require.False(t, c.Available())
	require.Empty(t, f.Commands(), "明文的命令一条都没发出去")
	// 真的 Redis（明文）也一样
	c, err = Open(Options{Addr: TestAddr(t), KeyPrefix: TestPrefix(t), Log: quiet(), TLS: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	require.False(t, c.Available())
}

func TestParseVersion(t *testing.T) {
	for in, want := range map[string]struct {
		ver   string
		major int
		ok    bool
	}{
		"redis_version:7.0.15":                             {"7.0.15", 7, true},
		"# Server\r\nredis_version:8.10.2\r\nos:Linux\r\n": {"8.10.2", 8, true},
		"redis_version:6":                                  {"6", 6, true},
		"valkey_version:8.0.1\nredis_version:7.2.4\n":      {"7.2.4", 7, true},
		"redis_version:":                                   {"", 0, false},
		"redis_version:x.1":                                {"", 0, false},
		"redis_version:-1.0":                               {"", 0, false},
		"version:7.0.0":                                    {"", 0, false},
		"":                                                 {"", 0, false},
	} {
		ver, major, ok := parseVersion(in)
		require.Equal(t, want.ok, ok, in)
		require.Equal(t, want.ver, ver, in)
		require.Equal(t, want.major, major, in)
	}
}

// 规范 §13.2 第 163 条（D-074）：启动时连不上不拒绝启动——按不可用处理，Do 立即返回 ErrUnavailable、不碰网络；
// 后台探测到它起来了自动恢复。connectWait 大于 0 时在这段时间里反复试，试到了就按正常连上处理。
func TestOpen_163_UnreachableStartsDegraded(t *testing.T) {
	addr := TestAddr(t)
	p := NewTestProxy(t, addr)
	p.Cut()
	start := time.Now()
	c, err := Open(Options{Addr: p.Addr(), KeyPrefix: TestPrefix(t), Log: quiet(), ProbeEvery: 20 * time.Millisecond})
	require.NoError(t, err, "连不上不是启动错误")
	t.Cleanup(func() { _ = c.Close() })
	require.Less(t, time.Since(start), 1500*time.Millisecond, "connectWait 为 0：只试一次")
	require.False(t, c.Available())

	ups := atomic.Int32{}
	c.OnUp(func() { ups.Add(1) })
	// 不可用期间：立即返回，不碰网络
	time.Sleep(60 * time.Millisecond) // 让在途的探测结束
	before := p.Accepts()
	begin := time.Now()
	for range 200 {
		require.ErrorIs(t, set(c, "k", "v"), ErrUnavailable)
	}
	took := time.Since(begin)
	require.Less(t, took, 200*time.Millisecond, "不可用时不该等网络")
	// 这期间新增的连接只来自后台探测（每 20 毫秒一次），不是来自这 200 次调用
	require.Less(t, p.Accepts()-before, 20)

	p.Restore()
	eventually(t, c.Available, "探测到 Redis 起来")
	require.NoError(t, set(c, "k", "v"))
	eventually(t, func() bool { return ups.Load() == 1 }, "恢复时调用一次回调")

	// connectWait：等的时候 Redis 起来了，就按正常连上处理
	p2 := NewTestProxy(t, addr)
	p2.Cut()
	time.AfterFunc(300*time.Millisecond, p2.Restore)
	start = time.Now()
	c2, err := Open(Options{Addr: p2.Addr(), KeyPrefix: TestPrefix(t), Log: quiet(), ConnectWait: 5 * time.Second})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c2.Close() })
	require.True(t, c2.Available())
	require.GreaterOrEqual(t, time.Since(start), 300*time.Millisecond)
	require.Less(t, time.Since(start), 3*time.Second, "连上了就不再等")

	// connectWait 到了还连不上：照常返回，不可用
	p3 := NewTestProxy(t, addr)
	p3.Cut()
	start = time.Now()
	c3, err := Open(Options{Addr: p3.Addr(), KeyPrefix: TestPrefix(t), Log: quiet(), ConnectWait: 1200 * time.Millisecond})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c3.Close() })
	require.False(t, c3.Available())
	require.GreaterOrEqual(t, time.Since(start), 700*time.Millisecond, "在 connectWait 里反复试")
	require.Less(t, time.Since(start), 4*time.Second)
}

// 规范 §13.2 第 163 条（D-074）：运行中出一次网络错误就标成不可用，之后立即返回、不碰网络；恢复后自动标回可用，
// 回调只在"从不可用恢复"时调用（启动时连上的那次不调），每次恢复调一次；回调 panic 不影响后面的回调和下一次恢复。
func TestDo_163_NetworkErrorMarksDownAndRecovers(t *testing.T) {
	p := NewTestProxy(t, TestAddr(t))
	var buf syncBuffer
	c, err := Open(Options{
		Addr: p.Addr(), KeyPrefix: TestPrefix(t), ProbeEvery: 10 * time.Millisecond,
		Log: slog.New(slog.NewTextHandler(&buf, nil)),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	ups := atomic.Int32{}
	c.OnUp(func() { panic("hook blew up") })
	c.OnUp(func() { ups.Add(1) })
	require.True(t, c.Available())
	require.NoError(t, set(c, "k", "v1"))
	require.Zero(t, ups.Load(), "启动时连上的不调回调")

	for round := 1; round <= 2; round++ {
		p.Cut()
		err = set(c, "k", "x")
		require.ErrorIs(t, err, ErrUnavailable, "第 %d 轮", round)
		require.False(t, c.Available())
		_, err = get(c, "k")
		require.ErrorIs(t, err, ErrUnavailable)

		p.Restore()
		eventually(t, c.Available, "恢复")
		eventually(t, func() bool { return int(ups.Load()) == round }, "回调")
		v, err := get(c, "k")
		require.NoError(t, err)
		require.Equal(t, "v1", v, "断开期间的写没有进去")
	}
	time.Sleep(100 * time.Millisecond)
	require.EqualValues(t, 2, ups.Load(), "可用的时候探测不再触发回调")
	logs := buf.String()
	require.Equal(t, 2, strings.Count(logs, "redis unavailable; falling back"), "每次断开记一条")
	require.Equal(t, 2, strings.Count(logs, "redis available again"))
	require.Equal(t, 2, strings.Count(logs, "recovery hook panicked"))
}

// 规范 §13.2 第 163 条（D-074）：服务器回"我现在用不了"（正在加载、只读、内存满、密码失效……）和网络错误一样
// 标成不可用；回普通的错误（不认识的命令、类型不对）不标。
func TestDo_163_ServerSaysItCannotServe(t *testing.T) {
	call := func(c *Client) error {
		return c.Do(context.Background(), func(ctx context.Context, rdb redis.Cmdable) error {
			return rdb.Get(ctx, c.Key("k")).Err()
		})
	}
	for _, reply := range []string{
		"-LOADING Redis is loading the dataset in memory",
		"-READONLY You can't write against a read only replica.",
		"-MISCONF Redis is configured to save RDB snapshots",
		"-OOM command not allowed when used memory > 'maxmemory'.",
		"-NOAUTH Authentication required.",
		"-WRONGPASS invalid username-password pair or user is disabled.",
		"-NOPERM this user has no permissions to run the 'get' command",
		"-MASTERDOWN Link with MASTER is down and replica-serve-stale-data is set to 'no'.",
		"-NOREPLICAS Not enough good replicas to write.",
		"-CLUSTERDOWN The cluster is down",
		"-BUSY Redis is busy running a script.",
		"-ERR max number of clients reached",
	} {
		f := NewTestFakeServer(t, "redis_version:7.2.4\r\n")
		c, err := openFake(t, f, Options{})
		require.NoError(t, err)
		require.True(t, c.Available())
		f.SetReply("GET", reply)
		err = call(c)
		require.ErrorIs(t, err, ErrUnavailable, reply)
		require.False(t, c.Available(), reply)
	}
	for _, reply := range []string{
		"-ERR unknown command 'GET'",
		"-WRONGTYPE Operation against a key holding the wrong kind of value",
		"-ERR syntax error",
		"-NOSCRIPT No matching script. Please use EVAL.",
	} {
		f := NewTestFakeServer(t, "redis_version:7.2.4\r\n")
		c, err := openFake(t, f, Options{})
		require.NoError(t, err)
		f.SetReply("GET", reply)
		err = call(c)
		require.Error(t, err, reply)
		require.NotErrorIs(t, err, ErrUnavailable, reply)
		require.True(t, c.Available(), reply)
	}
}

// 规范 §13.2 第 163 条（D-074）：Redis 假死（连得上、不回话）时一次操作在时限内返回并标成不可用，不拖住请求。
func TestDo_163_StalledServerTimesOut(t *testing.T) {
	p := NewTestProxy(t, TestAddr(t))
	c := OpenTest(t, p)
	require.NoError(t, set(c, "k", "v"))
	p.Stall()
	start := time.Now()
	err := set(c, "k", "x")
	require.ErrorIs(t, err, ErrUnavailable)
	require.Less(t, time.Since(start), 1500*time.Millisecond, "超时要有上限")
	require.False(t, c.Available())
	begin := time.Now()
	require.ErrorIs(t, set(c, "k", "x"), ErrUnavailable)
	require.Less(t, time.Since(begin), 50*time.Millisecond, "标成不可用之后立即返回")
	p.Restore()
	eventually(t, c.Available, "恢复")
}

// 规范 §13.2 第 163 条（D-074）：连接池里的连接被悄悄丢掉（没断、但再也收不到回应）之后，恢复不靠把池里的坏连接
// 一个个试过去——出过故障就换一个新的连接池，几个探测周期之内恢复。
func TestProbe_163_StalePoolIsReplaced(t *testing.T) {
	p := NewTestProxy(t, TestAddr(t))
	c := OpenTest(t, p)
	// 先让连接池里攒下一批空闲连接：同时占住 8 个连接，各发一条命令，再一起还回池里
	require.NoError(t, c.Do(context.Background(), func(ctx context.Context, rdb redis.Cmdable) error {
		cl, ok := rdb.(*redis.Client)
		require.True(t, ok)
		conns := make([]*redis.Conn, 8)
		for i := range conns {
			conns[i] = cl.Conn()
			if err := conns[i].Ping(ctx).Err(); err != nil {
				return err
			}
		}
		for _, cn := range conns {
			if err := cn.Close(); err != nil {
				return err
			}
		}
		return nil
	}))
	require.True(t, c.Available())
	require.GreaterOrEqual(t, p.Accepts(), 8)

	p.Freeze() // 现有的连接都哑了；新建的连接是好的
	require.ErrorIs(t, set(c, "k", "x"), ErrUnavailable)
	require.False(t, c.Available())
	start := time.Now()
	eventually(t, c.Available, "换了连接池之后恢复")
	require.Less(t, time.Since(start), 1500*time.Millisecond, "不是一个坏连接一个坏连接地试（每个要等一次超时）")
	require.NoError(t, set(c, "k", "v"))
}

// 规范 §13.2 第 163 条（D-074）：时好时坏的 Redis——刚恢复又出故障的，下一次探测前等得越来越久（恢复回调会清缓存，
// 不能每个探测周期清一遍）；稳定一段时间之后重新从头算。
func TestProbe_163_FlappingBacksOff(t *testing.T) {
	p := NewTestProxy(t, TestAddr(t))
	const every = 10 * time.Millisecond
	c, err := Open(Options{Addr: p.Addr(), KeyPrefix: TestPrefix(t), ProbeEvery: every, Log: quiet()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	flap := func() time.Duration {
		p.Cut()
		require.ErrorIs(t, set(c, "k", "x"), ErrUnavailable)
		p.Restore()
		start := time.Now()
		eventually(t, c.Available, "恢复")
		return time.Since(start)
	}
	first := flap()
	require.Less(t, first, 300*time.Millisecond, "头一次故障：很快恢复")
	var last time.Duration
	for range 5 {
		last = flap()
	}
	// 第 6 次：恢复之前先跳过 30 个探测周期（上限）
	require.Greater(t, last, 25*every, "连着出故障：越等越久（头一次 %v，这一次 %v）", first, last)
	require.Less(t, last, 60*every+time.Second, "等待有上限")

	// 稳定够久（60 个探测周期）之后再出故障：从头算，又是很快恢复
	time.Sleep(60*every + 100*time.Millisecond)
	require.Less(t, flap(), 300*time.Millisecond)
}

// 规范 §13.2 第 163 条（D-074）：探测通一次不算恢复，要连续通两次（PING、读版本、试写都过才算一次）。
func TestProbe_163_NeedsTwoGoodProbes(t *testing.T) {
	f := NewTestFakeServer(t, "redis_version:7.2.4\r\n")
	c, err := openFake(t, f, Options{})
	require.NoError(t, err)
	probes := func() int {
		n := 0
		for _, l := range f.Lines() {
			if strings.HasPrefix(l, "SET t:probe ") {
				n++
			}
		}
		return n
	}
	pings := func() int {
		n := 0
		for _, name := range f.Commands() {
			if name == "PING" {
				n++
			}
		}
		return n
	}
	const loading = "-LOADING Redis is loading the dataset in memory"
	f.SetReply("PING", loading)
	f.SetReply("GET", loading)
	_, err = get(c, "k")
	require.ErrorIs(t, err, ErrUnavailable)
	before := pings()
	eventually(t, func() bool { return pings() >= before+2 }, "不可用期间在探测")
	require.False(t, c.Available())
	base := probes()

	f.SetReply("PING", "")
	eventually(t, c.Available, "恢复")
	require.Equal(t, 2, probes()-base, "连续通两次才标回可用")
}

// 时好时坏时探测前多等的周期数：一次比一次久，有上限。
func TestHoldPeriods(t *testing.T) {
	for steps, want := range map[int]int{-1: 0, 0: 0, 1: 1, 2: 3, 3: 7, 4: 15, 5: 30, 6: 30, 8: 30, 9: 30, 1000: 30} {
		require.Equal(t, want, holdPeriods(steps), "steps=%d", steps)
	}
}

// 规范 §13.2 第 163 条（D-074）：请求自己被取消（客户端走了）不算 Redis 的问题，不标成不可用——
// 不管是调用之前就取消了，还是等回复的时候取消的。
func TestDo_163_CallerCancelIsNotAnOutage(t *testing.T) {
	c := OpenTest(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := c.Do(ctx, func(ctx context.Context, rdb redis.Cmdable) error {
		return rdb.Set(ctx, c.Key("k"), "v", time.Minute).Err()
	})
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrUnavailable)
	require.True(t, c.Available())

	ctx, cancel = context.WithCancel(context.Background())
	time.AfterFunc(30*time.Millisecond, cancel)
	err = c.Do(ctx, func(ctx context.Context, rdb redis.Cmdable) error {
		return rdb.BLPop(ctx, 250*time.Millisecond, c.Key("empty")).Err()
	})
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrUnavailable)
	require.True(t, c.Available())
	require.NoError(t, set(c, "k", "v"))
}

// 规范 §13.2 第 163 条（D-074）：启动时没连上、后来连上的是个老版本——一直按不可用处理，同一个原因只记一次；
// 换成够新的版本后恢复。
func TestProbe_163_TooOldAtRuntimeStaysDown(t *testing.T) {
	f := NewTestFakeServer(t, "redis_version:5.0.14\r\n")
	p := NewTestProxy(t, f.Addr())
	p.Cut()
	var buf syncBuffer
	c, err := Open(Options{Addr: p.Addr(), KeyPrefix: "t:", ProbeEvery: 10 * time.Millisecond, Log: slog.New(slog.NewTextHandler(&buf, nil))})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	require.False(t, c.Available())
	p.Restore()
	eventually(t, func() bool { return strings.Contains(buf.String(), "5.0.14") }, "探测到老版本")
	time.Sleep(150 * time.Millisecond)
	require.False(t, c.Available(), "版本太低：不可用")
	require.Equal(t, 1, strings.Count(buf.String(), "5.0.14"), "同一个原因只记一次，不是每次探测都记")
	f.SetInfo("redis_version:7.2.4\r\n")
	eventually(t, c.Available, "版本够了之后恢复")
	require.Equal(t, "7.2.4", c.Version())
}

// Close 之后不可用；可以重复调用；启动时没连上的也能马上关。
func TestClose(t *testing.T) {
	c := OpenTest(t, nil)
	require.NoError(t, set(c, "k", "v"))
	require.NoError(t, c.Close())
	require.False(t, c.Available())
	require.ErrorIs(t, set(c, "k", "v"), ErrUnavailable)
	require.NoError(t, c.Close())

	p := NewTestProxy(t, TestAddr(t))
	p.Cut()
	down, err := Open(Options{Addr: p.Addr(), KeyPrefix: "t:", Log: quiet()})
	require.NoError(t, err)
	start := time.Now()
	require.NoError(t, down.Close())
	require.Less(t, time.Since(start), 3*time.Second)
}

// 密码不进日志：连不上、被拒绝、恢复，哪一条日志里都没有。
func TestOpen_163_PasswordNotLogged(t *testing.T) {
	p := NewTestProxy(t, TestAddr(t))
	p.Cut()
	var buf syncBuffer
	c, err := Open(Options{
		Addr: p.Addr(), KeyPrefix: TestPrefix(t), Password: "s3cret-redis-pass",
		Log: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), ProbeEvery: 10 * time.Millisecond,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	time.Sleep(50 * time.Millisecond)
	p.Restore()
	eventually(t, c.Available, "恢复")
	require.Contains(t, buf.String(), "redis unavailable at startup")
	require.Contains(t, buf.String(), "redis available again")
	require.NotContains(t, buf.String(), "s3cret-redis-pass")

	// 运行中被服务器拒绝（密码被换掉了）：探测的日志里带服务器的说法，不带密码
	f := NewTestFakeServer(t, "redis_version:7.2.4\r\n")
	var buf2 syncBuffer
	c2, err := openFake(t, f, Options{Username: "app", Password: "s3cret-redis-pass", Log: slog.New(slog.NewTextHandler(&buf2, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	require.NoError(t, err)
	f.SetReply("GET", "-WRONGPASS invalid username-password pair or user is disabled.")
	f.SetReply("PING", "-WRONGPASS invalid username-password pair or user is disabled.")
	_, err = get(c2, "k")
	require.ErrorIs(t, err, ErrUnavailable)
	eventually(t, func() bool { return strings.Contains(buf2.String(), "redis still unavailable") }, "探测被拒绝")
	require.Contains(t, buf2.String(), "WRONGPASS")
	require.NotContains(t, buf2.String(), "s3cret-redis-pass")
	require.NotContains(t, err.Error(), "s3cret-redis-pass")
}

// syncBuffer 是并发安全的日志缓冲。
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
