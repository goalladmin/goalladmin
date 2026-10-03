// Package redisx 是可选的 Redis 连接（D-074）：让同一个程序的多个实例、以及平台、代理商、商户三个程序之间
// 共用一部分状态（失效通知、限流和计数、验证码的答案）。
//
// 几条约定，用它的代码都要守：
//   - Redis 不可用时一律退回本实例的内存，服务照常。Do 返回任何错误（redis.Nil 除外）时调用方都这样处理。
//   - 出一次故障（网络错误，或者服务器明说自己现在用不了）就把 Redis 标成不可用，之后的 Do 立即返回 ErrUnavailable、
//     不碰网络——不让每个请求都等一次超时；后台定时探测，连续通两次再标回可用，并调用 OnUp 登记的回调
//     （断开期间的通知收不到，缓存要清一遍）。刚恢复又出故障的，下一次探测前等得更久：一个时好时坏的 Redis
//     不该让缓存每秒被清一遍。
//   - 只用 Redis 6.0 就有的命令。不在 Do 里用会阻塞的命令（BLPOP 之类）：等得比一次操作的时限久，会被当成故障。
package redisx

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/maintnotifications"
)

// MinMajor 是支持的 Redis 服务器最低主版本（D-074）。
const MinMajor = 6

// 每次操作的时限（D-074 第 9 条）：超时就按不可用处理、退回内存，不让一个慢的 Redis 拖住请求。不重试。
const (
	dialTimeout = time.Second
	ioTimeout   = 300 * time.Millisecond // 单次读、单次写、等连接池里的连接
	opTimeout   = 500 * time.Millisecond // 一次 Do 的上限（包括要新建连接的那一次）
	// 探测一次（PING、读版本、试写、发布订阅）的上限
	checkTimeout = 2 * time.Second
	// 启动时等 Redis 就绪，每隔这么久试一次
	startRetry = 500 * time.Millisecond
	// 不可用时每隔这么久探测一次
	defaultProbeEvery = time.Second
	// 连续通这么多次才标回可用
	upAfter = 2
	// 恢复之后这么多个探测周期之内又出故障，算"时好时坏"：下一次探测前多等一倍，最多等 maxHold 个周期；
	// 稳定了这么久之后重新从头算
	stablePeriods = 60
	maxHold       = 30
	// 一次故障期间最多记这么多种不同的原因，再多的不记（时好时坏、原因五花八门时不刷屏）
	maxReasons = 8
)

// quietClient 让客户端库自己的日志走 slog（Debug 级别），不直接往标准错误输出写——它在每次连不上时都会写一行，
// 格式和程序的日志不一样。库的日志器是进程级的，只装一次。
var quietClient sync.Once //nolint:gochecknoglobals // 只用来保证"装一次"，不承载状态

type clientLog struct{}

func (clientLog) Printf(ctx context.Context, format string, v ...any) {
	slog.Default().DebugContext(ctx, "redis client", "msg", fmt.Sprintf(format, v...))
}

// ErrUnavailable 表示 Redis 现在不可用（没连上、刚出过故障、已经关闭）。调用方退回本实例的内存。
var ErrUnavailable = errors.New("redisx: redis 不可用")

// TooOldError 表示服务器版本低于 MinMajor。启动时遇到它拒绝启动。
type TooOldError struct{ Version string }

func (e *TooOldError) Error() string {
	return fmt.Sprintf("redisx: redis 服务器版本 %s 低于最低要求 %d.0", e.Version, MinMajor)
}

// RejectedError 表示服务器明确拒绝了这份配置：密码或用户名不对、没有权限、库号不存在。启动时遇到它拒绝启动
// ——这是配置写错了，不是 Redis 暂时不可用。错误信息来自服务器，不含密码。
type RejectedError struct{ Err error }

func (e *RejectedError) Error() string {
	hint := ""
	if isAuthArity(e.Err) {
		hint = "；这个服务器不认带用户名的登录，多半低于最低要求的 " + strconv.Itoa(MinMajor) + ".0"
	}
	return fmt.Sprintf("redisx: redis 拒绝了连接（检查 redis.username、redis.password、redis.db 和这个账号的权限%s）: %v", hint, e.Err)
}

func (e *RejectedError) Unwrap() error { return e.Err }

// Options 是连接参数，来自配置 redis.*（conf.Validate 已经校验过）。
type Options struct {
	Addr        string
	Username    string
	Password    string
	DB          int
	TLS         bool
	KeyPrefix   string
	ConnectWait time.Duration // 启动时等 Redis 就绪的最长时间
	Log         *slog.Logger
	// ProbeEvery 是不可用时探测的间隔，只有测试会设；0 用默认值（1 秒）
	ProbeEvery time.Duration
	// Check 补充探测当前业务使用的脚本与命令权限，启动和故障恢复都执行。
	Check func(context.Context, redis.Cmdable, func(...string) string) error
	// rootCAs 是校验服务器证书用的根证书，只有测试会设；nil 用系统的
	rootCAs *x509.CertPool
}

// Client 是一个 Redis 连接（内部是连接池）和它的可用状态。所有方法并发安全。
type Client struct {
	ro         *redis.Options
	rdb        atomic.Pointer[redis.Client]
	prefix     string
	secret     string // 密码：只用来把它从错误信息里抹掉
	log        *slog.Logger
	checkExtra func(context.Context, redis.Cmdable, func(...string) string) error

	up        atomic.Bool  // 现在认为 Redis 可用
	stale     atomic.Bool  // 出过故障：下一次探测前换一个新的连接池（旧池里的连接多半已经坏了）
	version   atomic.Value // string：读到的服务器版本；空串表示还没读到或读不出来
	noVersion atomic.Bool  // 已经为"读不出版本"记过警告

	mu   sync.Mutex
	onUp []func()

	probeEvery time.Duration
	base       context.Context // Close 时取消：在途的探测随之中止
	cancel     context.CancelFunc
	done       chan struct{}
	closeOnce  sync.Once
}

// Open 建立连接并启动后台探测。
//
//   - 连上了、版本低于 MinMajor：返回 *TooOldError；服务器拒绝了这份配置（密码不对、没有权限、库号不存在）：
//     返回 *RejectedError。这两种是配置错误，调用方拒绝启动。
//   - 连不上，或者服务器暂时用不了（正在加载数据等）：最多等 o.ConnectWait，还不行就返回一个处于不可用状态的 Client
//     （不返回错误）——Redis 挂着的时候重启一个实例不该起不来；后台继续探测，通了自动恢复。
func Open(o Options) (*Client, error) {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	quietClient.Do(func() { redis.SetLogger(clientLog{}) })
	ro := &redis.Options{
		Addr:     o.Addr,
		Username: o.Username,
		Password: o.Password,
		DB:       o.DB,
		// RESP2、不发客户端标识、不开维护通知：6.0 和各家的代理接入都认（D-074 第 10 条）
		Protocol:                 2,
		DisableIdentity:          true,
		MaintNotificationsConfig: &maintnotifications.Config{Mode: maintnotifications.ModeDisabled},
		// 不重试：失败了由 Do 标成不可用、调用方退回内存
		MaxRetries:            -1,
		DialerRetries:         1,
		DialTimeout:           dialTimeout,
		ReadTimeout:           ioTimeout,
		WriteTimeout:          ioTimeout,
		PoolTimeout:           ioTimeout,
		ContextTimeoutEnabled: true,
	}
	if o.TLS {
		host, _, _ := net.SplitHostPort(o.Addr)
		ro.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host, RootCAs: o.rootCAs}
	}
	// 建立连接这一步的失败单独标出来（见 dialError）
	dial := redis.NewDialer(ro)
	ro.Dialer = func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := dial(ctx, network, addr)
		if err != nil {
			return nil, &dialError{err: err}
		}
		return conn, nil
	}
	c := &Client{
		ro:         ro,
		prefix:     o.KeyPrefix,
		secret:     o.Password,
		log:        o.Log.With("component", "redis", "addr", o.Addr),
		probeEvery: o.ProbeEvery,
		checkExtra: o.Check,
		done:       make(chan struct{}),
	}
	c.rdb.Store(redis.NewClient(ro))
	c.base, c.cancel = context.WithCancel(context.Background())
	c.version.Store("")
	if c.probeEvery <= 0 {
		c.probeEvery = defaultProbeEvery
	}

	deadline := time.Now().Add(o.ConnectWait)
	for {
		err := c.check()
		if err == nil {
			c.up.Store(true)
			c.log.Info("redis connected", "version", c.Version())
			break
		}
		if fatal := c.startupError(err); fatal != nil {
			c.cancel()
			_ = c.rdb.Load().Close()
			return nil, fatal
		}
		if !time.Now().Add(startRetry).Before(deadline) {
			// 等不到：照常启动，按不可用处理（D-074 第 8 条）
			c.stale.Store(true)
			c.log.Error("redis unavailable at startup; continuing with in-process state until it comes back", "err", c.safeErr(err))
			break
		}
		time.Sleep(startRetry)
	}
	go c.probe()
	return c, nil
}

// startupError 把探测的错误分成两类：配置错误（返回它，拒绝启动）和暂时不可用（返回 nil）。
func (c *Client) startupError(err error) error {
	var old *TooOldError
	if errors.As(err, &old) {
		return err
	}
	if isRejection(err) {
		return &RejectedError{Err: c.safeErr(err)}
	}
	return nil
}

// Available 报告 Redis 现在是否可用。
func (c *Client) Available() bool { return c.up.Load() }

// Version 返回读到的服务器版本；读不出来时是空串。
func (c *Client) Version() string {
	v, _ := c.version.Load().(string)
	return v
}

// Key 把几段拼成带前缀的键：<前缀><段1>:<段2>…。
func (c *Client) Key(parts ...string) string { return c.prefix + strings.Join(parts, ":") }

// OnUp 登记一个回调：每次 Redis 从不可用恢复时调用（启动时就连上的那一次不调）。断开期间别的实例发的通知
// 收不到，登记方在这里把靠通知保持新鲜的缓存清掉。回调在探测协程里执行：要快、不能阻塞，也不能调 Close。
func (c *Client) OnUp(fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onUp = append(c.onUp, fn)
}

// Do 在 Redis 可用时执行 fn。不可用时立即返回 ErrUnavailable，不碰网络。
//
// fn 返回的错误分三种：
//   - 故障——网络层面的错误（连不上、超时、连接断了），或者服务器明说自己现在用不了（正在加载、只读、密码失效……）：
//     把 Redis 标成不可用，返回包着 ErrUnavailable 的错误。
//   - 请求自己结束了（调用方的 ctx 被取消或到了时限）、连接池一时取不到连接、别的错误（redis.Nil、脚本出错、
//     类型不对、fn 自己的错误）：原样返回，不改可用状态。
//
// 不管哪种错误（redis.Nil 除外），调用方都退回本实例的内存。
func (c *Client) Do(ctx context.Context, fn func(ctx context.Context, rdb redis.Cmdable) error) error {
	if !c.up.Load() {
		return ErrUnavailable
	}
	rdb := c.rdb.Load()
	callerDeadline, hasDeadline := ctx.Deadline()
	opCtx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	err := fn(opCtx, rdb)
	if err == nil || errors.Is(err, redis.Nil) {
		return err
	}
	err = c.safeErr(err)
	// 调用方的时限也要按时刻比：连接上的读写时限和 ctx 的定时器各走各的，读超时返回的那一刻 ctx.Err() 可能还是空的
	if callerErr := ctx.Err(); callerErr != nil {
		return errors.Join(callerErr, err)
	}
	if hasDeadline && !time.Now().Before(callerDeadline) {
		return errors.Join(context.DeadlineExceeded, err)
	}
	closed := errors.Is(err, redis.ErrClosed)
	if !closed && !isOutage(err) {
		return err
	}
	// 池身份的判断和状态切换在同一把锁里：旧池迟到的错误不能影响新池。
	c.markDown(rdb, err)
	return fmt.Errorf("%w: %w", ErrUnavailable, err)
}

// Close 停止探测并关闭连接。
func (c *Client) Close() error {
	var err error
	c.closeOnce.Do(func() {
		c.cancel()
		<-c.done
		c.up.Store(false)
		err = c.rdb.Load().Close()
	})
	return err
}

// isReply 报告 err 是不是服务器回的错误（脚本出错、类型不对、没有权限……）：能收到回复，说明连接是通的。
func isReply(err error) bool {
	var re redis.Error
	return errors.As(err, &re)
}

// replyHasPrefix 报告服务器回的错误是不是以其中一个词开头。
func replyHasPrefix(err error, prefixes ...string) bool {
	var re redis.Error
	if !errors.As(err, &re) {
		return false
	}
	msg := re.Error()
	for _, p := range prefixes {
		if strings.HasPrefix(msg, p) {
			return true
		}
	}
	return false
}

// isAuthArity 报告 err 是不是 6.0 之前的服务器对"带用户名的 AUTH"的回复：它只认一个参数。
func isAuthArity(err error) bool {
	return replyHasPrefix(err, "ERR wrong number of arguments for 'auth'", "ERR wrong number of arguments for 'AUTH'")
}

// isRejection 报告服务器是不是明确拒绝了这份配置：密码或用户名不对、没设密码却发了密码、没有权限、库号不存在、
// 不认带用户名的登录。
func isRejection(err error) bool {
	// Redis 6 的脚本内 ACL 拒绝带脚本位置，不能只按 NOPERM 前缀识别。
	var reply redis.Error
	if errors.As(err, &reply) {
		message := reply.Error()
		if strings.HasPrefix(message, "ERR Error running script") &&
			(strings.Contains(message, "NOPERM") || strings.Contains(message, "The user executing the script can't run this command") ||
				strings.Contains(message, "The user executing the script can't access")) {
			return true
		}
	}
	return replyHasPrefix(err, "NOAUTH", "WRONGPASS", "NOPERM", "ERR invalid password", "ERR AUTH", "ERR Client sent AUTH",
		"ERR invalid username-password", "ERR DB index is out of range", "ERR invalid DB index") ||
		isAuthArity(err)
}

// dialError 标出"建立连接这一步失败了"。不管原因是什么——连不上、TLS 握手不成、证书验不过（过期、换了签发方）——
// Redis 现在都用不了；后两种不是 net.Error，不标出来的话会一直被当成普通错误、可用状态不变。
type dialError struct{ err error }

func (e *dialError) Error() string { return e.err.Error() }
func (e *dialError) Unwrap() error { return e.err }

// redactedError 是抹掉了密码的错误：Error() 给出的文字里没有密码，Unwrap 之后仍然是原来的错误（分类要用）。
type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

// safeErr 把服务器回的错误里出现的密码换成 ***。真的 Redis 不会把 AUTH 的参数回显出来，但不认识这条命令的
// 接入层可能回"不认识的命令……参数是……"；错误要进日志、要返回给调用方，先抹掉。
func (c *Client) safeErr(err error) error {
	if err == nil || c.secret == "" || !isReply(err) || !strings.Contains(err.Error(), c.secret) {
		return err
	}
	return &redactedError{msg: strings.ReplaceAll(err.Error(), c.secret, "***"), err: err}
}

// reason 把一个错误归成"原因"，用来判断是不是已经记过：去掉每次都不一样的部分（本机的临时端口）。
func reason(err error) string {
	var op *net.OpError
	if errors.As(err, &op) && op.Err != nil {
		return op.Op + " " + op.Net + ": " + op.Err.Error()
	}
	return err.Error()
}

// isOutage 报告 err 是不是说明 Redis 现在用不了：网络层面的错误，或者服务器回的"我现在不能服务"。
// 连接池一时取不到连接（并发太高）不算：那是这一次请求的事。别的回复（脚本出错、类型不对）和 fn 自己的错误也不算。
func isOutage(err error) bool {
	if errors.Is(err, redis.ErrPoolTimeout) || errors.Is(err, redis.ErrPoolExhausted) {
		return false
	}
	if isReply(err) {
		return isRejection(err) || replyHasPrefix(err, "LOADING", "READONLY", "MISCONF", "MASTERDOWN", "CLUSTERDOWN",
			"NOREPLICAS", "OOM", "BUSY", "TRYAGAIN", "ERR max number of clients")
	}
	var de *dialError
	if errors.As(err, &de) {
		return true
	}
	// 超时也在这里：读写超时、context.DeadlineExceeded 都实现了 net.Error
	var ne net.Error
	return errors.As(err, &ne) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

// holdPeriods 返回"时好时坏"了 steps 次之后，下一次探测前先跳过多少个探测周期：0、1、3、7、15，最多 maxHold。
func holdPeriods(steps int) int {
	return min(1<<min(max(steps, 0), 8)-1, maxHold)
}

// markDown 在连接池仍是 rdb 时把 Redis 标成不可用。订阅的接收循环也用这个入口报告故障。
func (c *Client) markDown(rdb *redis.Client, err error) {
	c.mu.Lock()
	changed := c.base.Err() == nil && c.rdb.Load() == rdb && c.up.CompareAndSwap(true, false)
	if changed {
		c.stale.Store(true)
	}
	c.mu.Unlock()
	if changed {
		c.log.Error("redis unavailable; falling back to in-process state", "err", c.safeErr(err))
	}
}

func (c *Client) markUp() {
	c.mu.Lock()
	if c.base.Err() != nil || !c.up.CompareAndSwap(false, true) {
		c.mu.Unlock()
		return
	}
	hooks := append([]func(){}, c.onUp...)
	c.mu.Unlock()
	c.log.Info("redis available again", "version", c.Version())
	for _, fn := range hooks {
		c.runHook(fn)
	}
}

// runHook 执行一个恢复回调；回调 panic 不能把探测协程（和整个进程）带走。
func (c *Client) runHook(fn func()) {
	defer func() {
		if r := recover(); r != nil {
			c.log.Error("redis recovery hook panicked", "panic", fmt.Sprint(r))
		}
	}()
	fn()
}

// probe 在 Redis 不可用时定时探测，连续通 upAfter 次就标回可用。可用的时候不发任何命令。
func (c *Client) probe() {
	defer close(c.done)
	t := time.NewTicker(c.probeEvery)
	defer t.Stop()
	var (
		good      int                     // 连续通了几次
		logged    = map[string]struct{}{} // 这次故障期间已经记进日志的原因：同一个原因只记一次
		wasUp     = c.up.Load()
		lastUp    time.Time // 上一次标回可用的时刻
		hold      int       // 这次故障之后先跳过多少个探测周期
		holdSteps int       // 时好时坏的次数：决定 hold
	)
	for {
		select {
		case <-c.base.Done():
			return
		case <-t.C:
		}
		if c.up.Load() {
			wasUp = true
			continue
		}
		if wasUp {
			// 刚从可用变成不可用：恢复没多久就又坏了的，多等一会儿再探测
			wasUp, good = false, 0
			if !lastUp.IsZero() && time.Since(lastUp) < stablePeriods*c.probeEvery {
				holdSteps++
			} else {
				holdSteps = 0
			}
			hold = holdPeriods(holdSteps)
		}
		if hold > 0 {
			hold--
			continue
		}
		if c.stale.CompareAndSwap(true, false) {
			// 换一个新的连接池：旧池里的连接多半已经坏了，一个个试过去要很久
			c.mu.Lock()
			old := c.rdb.Swap(redis.NewClient(c.ro))
			c.mu.Unlock()
			_ = old.Close()
		}
		err := c.check()
		if c.base.Err() != nil {
			// 正在关闭：探测是被取消的，不是 Redis 的问题
			return
		}
		if err != nil {
			good = 0
			c.stale.Store(true)
			if _, done := logged[reason(err)]; !done && len(logged) < maxReasons {
				logged[reason(err)] = struct{}{}
				c.log.Error("redis still unavailable", "err", c.safeErr(err))
			}
			continue
		}
		good++
		if good < upAfter {
			continue
		}
		lastUp, wasUp = time.Now(), true
		clear(logged)
		c.markUp()
	}
}

// check 探测一次：PING 通、版本不低于 MinMajor、写得进去、发布订阅也可用才算可用。
// 版本读不出来（接入层不让查）记一条警告、照常用。
//
// 往后加了新的用法（脚本等）时，把它要的命令也加进这里试一遍：账号缺哪条命令的权限，启动时就拒绝，
// 而不是运行中用到的时候才标成不可用、探测又通、再用又不可用地来回跳。
func (c *Client) check() error {
	ctx, cancel := context.WithTimeout(c.base, checkTimeout)
	defer cancel()
	rdb := c.rdb.Load()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return err
	}
	info, err := rdb.Info(ctx, "server").Result()
	if err != nil && (!isReply(err) || (isOutage(err) && !replyHasPrefix(err, "NOPERM"))) {
		return err
	}
	// err 不为空时是服务器回的"这条命令不让用"（不认识、这个账号没有权限）：连接是通的，只是不让查
	ver, major, ok := parseVersion(info)
	switch {
	case err != nil || !ok:
		c.version.Store("")
		if c.noVersion.CompareAndSwap(false, true) {
			c.log.Warn("cannot read redis server version; assuming it is supported", "min", MinMajor, "err", c.safeErr(err))
		}
	case major < MinMajor:
		return &TooOldError{Version: ver}
	default:
		c.version.Store(ver)
	}
	// 试写一次：只读的副本、内存满了、落盘出错、账号没有写权限，PING 都是通的，写才看得出来
	if err := rdb.Set(ctx, c.Key("probe"), "1", time.Minute).Err(); err != nil {
		return err
	}
	if err := c.checkSubscription(ctx, rdb); err != nil {
		return err
	}
	if c.checkExtra != nil {
		return c.checkExtra(ctx, rdb, c.Key)
	}
	return nil
}

// parseVersion 从 INFO server 的输出里取 redis_version，返回版本字符串和主版本号。
func parseVersion(info string) (ver string, major int, ok bool) {
	for _, line := range strings.Split(info, "\n") {
		v, found := strings.CutPrefix(strings.TrimSpace(line), "redis_version:")
		if !found {
			continue
		}
		head, _, _ := strings.Cut(v, ".")
		n, err := strconv.Atoi(head)
		if err != nil || n < 0 {
			return "", 0, false
		}
		return v, n, true
	}
	return "", 0, false
}
