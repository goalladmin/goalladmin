package redisx

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// TestAddrEnv 是测试用 Redis 地址（主机:端口）的环境变量名（Makefile 里有默认值）。
const TestAddrEnv = "GA_TEST_REDIS"

// TestAddr 返回测试用 Redis 的地址；未设置 GA_TEST_REDIS 时跳过测试。
//
// 这个文件放在非 _test 文件里，是为了让其他包的测试也能用（和 db.OpenTestDB 一样）。
func TestAddr(t testing.TB) string {
	t.Helper()
	addr := os.Getenv(TestAddrEnv)
	if addr == "" {
		t.Skipf("未设置 %s，跳过需要 Redis 的测试", TestAddrEnv)
	}
	return addr
}

// TestPrefix 返回一个这次测试独有的键前缀，测试结束时删掉这个前缀下的所有键。
func TestPrefix(t testing.TB) string {
	t.Helper()
	addr := TestAddr(t)
	var b [6]byte
	_, _ = rand.Read(b[:])
	prefix := "gatest:" + hex.EncodeToString(b[:]) + ":"
	t.Cleanup(func() {
		rdb := redis.NewClient(&redis.Options{Addr: addr, Protocol: 2, DisableIdentity: true})
		defer func() { _ = rdb.Close() }()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		iter := rdb.Scan(ctx, 0, prefix+"*", 500).Iterator()
		for iter.Next(ctx) {
			_ = rdb.Del(ctx, iter.Val()).Err()
		}
		if err := iter.Err(); err != nil {
			t.Logf("清理测试键失败: %v", err)
		}
	})
	return prefix
}

// OpenTest 连接测试用 Redis：键前缀是这次测试独有的，测试结束时清掉这些键并关闭连接。
// via 不为空时经过它连（见 TestProxy），否则直连。
func OpenTest(t testing.TB, via *TestProxy) *Client {
	t.Helper()
	addr := TestAddr(t)
	prefix := TestPrefix(t)
	if via != nil {
		addr = via.Addr()
	}
	c, err := Open(Options{
		Addr: addr, KeyPrefix: prefix, ConnectWait: 2 * time.Second, ProbeEvery: 20 * time.Millisecond,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("连接测试用 Redis 失败: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestProxy 是测试里夹在客户端和 Redis 中间的转发器，用来模拟 Redis 不可用：
// Cut 之后已有的连接全部断开、新的连接一连上就被关掉；Stall 之后连接照样能建立，但什么都不回（服务器假死）；
// Freeze 让已有的连接变成"没断、但再也收不到回应"（中间设备悄悄丢掉了连接），新的连接照常转发；
// Restore 恢复转发。
type TestProxy struct {
	ln     net.Listener
	target string

	mu      sync.Mutex
	mode    int // 0 转发，1 切断，2 假死
	conns   map[net.Conn]struct{}
	accepts int
	frozen  atomic.Int64 // 冻结的代数：比它老的连接不再转发数据
	gen     atomic.Int64
}

const (
	proxyForward = iota
	proxyCut
	proxyStall
)

// NewTestProxy 在本机随机端口上起一个转发到 target 的转发器，测试结束时关闭。
func NewTestProxy(t testing.TB, target string) *TestProxy {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	p := &TestProxy{ln: ln, target: target, conns: map[net.Conn]struct{}{}}
	go p.serve()
	t.Cleanup(func() {
		_ = ln.Close()
		p.closeAll()
	})
	return p
}

// Addr 返回转发器的地址，客户端连它。
func (p *TestProxy) Addr() string { return p.ln.Addr().String() }

// Cut 模拟 Redis 挂了：断开所有连接，之后的连接一连上就被关掉。
func (p *TestProxy) Cut() { p.setMode(proxyCut) }

// Stall 模拟 Redis 假死：断开已有的连接，之后的连接能建立，但发什么都没有回应。
func (p *TestProxy) Stall() { p.setMode(proxyStall) }

// Restore 恢复转发。
func (p *TestProxy) Restore() { p.setMode(proxyForward) }

// Accepts 返回到现在为止接受过多少个连接：用来确认"不可用期间没有碰网络"。
func (p *TestProxy) Accepts() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.accepts
}

// Freeze 模拟连接被悄悄丢掉：已有的连接不断开，但两个方向的数据都不再转发；之后新建的连接照常转发。
func (p *TestProxy) Freeze() { p.frozen.Store(p.gen.Load()) }

// setMode 换模式并关掉此前的连接。换模式和摘下旧连接在同一把锁里：换成转发之后才接进来的连接不会被误关。
func (p *TestProxy) setMode(m int) {
	p.mu.Lock()
	old := p.conns
	p.conns = map[net.Conn]struct{}{}
	p.mode = m
	p.mu.Unlock()
	for c := range old {
		_ = c.Close()
	}
}

func (p *TestProxy) closeAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for c := range p.conns {
		_ = c.Close()
	}
	p.conns = map[net.Conn]struct{}{}
}

func (p *TestProxy) serve() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		p.accepts++
		mode := p.mode
		if mode == proxyStall {
			p.conns[c] = struct{}{} // 留着不回应，换模式时关掉
		}
		p.mu.Unlock()
		switch mode {
		case proxyCut:
			_ = c.Close()
		case proxyStall:
		default:
			go p.pipe(c)
		}
	}
}

func (p *TestProxy) pipe(c net.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	up, err := (&net.Dialer{}).DialContext(ctx, "tcp", p.target)
	if err != nil {
		_ = c.Close()
		return
	}
	// 拨号期间可能已经被切断：登记和判断放在同一把锁里，不留漏网的连接
	p.mu.Lock()
	if p.mode != proxyForward {
		p.mu.Unlock()
		_ = c.Close()
		_ = up.Close()
		return
	}
	p.conns[c], p.conns[up] = struct{}{}, struct{}{}
	p.mu.Unlock()
	gen := p.gen.Add(1)
	go func() { p.relay(up, c, gen); _ = up.Close(); _ = c.Close() }()
	p.relay(c, up, gen)
	_ = c.Close()
	_ = up.Close()
}

// relay 把 src 读到的转给 dst；这条连接被冻结之后读到的都丢掉。
func (p *TestProxy) relay(dst, src net.Conn, gen int64) {
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 && gen > p.frozen.Load() {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// TestFakeServer 是一个只会连接探测、计数权限探测和发布订阅的假 Redis（RESP2）：用来报出指定的版本、或者不让查版本，
// 并记下收到的命令。别的命令（包括 HELLO）一律回"不认识的命令"，和 6.0 之前的服务器一样——客户端会退到旧的握手方式。
type TestFakeServer struct {
	ln      net.Listener
	mu      sync.Mutex
	info    string
	replies map[string]string // 命令名（大写）→ 整条回复（不带结尾的 \r\n）；没有的用默认回复
	seen    []string
	lines   []string
}

// NewTestFakeServer 在本机随机端口上起一个假 Redis，测试结束时关闭。info 是 INFO 的回复：
// 以 "-" 开头时当作错误回复（如 "-NOPERM …"），"!drop" 表示收到 INFO 就断开连接，否则是正文（如 "redis_version:5.0.14\r\n"）。
func NewTestFakeServer(t testing.TB, info string) *TestFakeServer {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	f := &TestFakeServer{ln: ln, info: info}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

// Addr 返回假服务器的地址。
func (f *TestFakeServer) Addr() string { return f.ln.Addr().String() }

// SetInfo 换掉 INFO 的回复。
func (f *TestFakeServer) SetInfo(info string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.info = info
}

// SetReply 指定某条命令的回复（RESP 的一行，如 "-NOAUTH Authentication required." 或 "+OK"）；reply 为空时恢复默认。
func (f *TestFakeServer) SetReply(cmd, reply string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.replies == nil {
		f.replies = map[string]string{}
	}
	if reply == "" {
		delete(f.replies, strings.ToUpper(cmd))
		return
	}
	f.replies[strings.ToUpper(cmd)] = reply
}

// Commands 返回收到过的命令名（大写）。
func (f *TestFakeServer) Commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.seen...)
}

// Lines 返回收到过的命令，连同参数（空格分隔，命令名大写）。
func (f *TestFakeServer) Lines() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.lines...)
}

func (f *TestFakeServer) serve(c net.Conn) {
	defer func() { _ = c.Close() }()
	r := bufio.NewReader(c)
	channel := ""
	for {
		args, err := readTestCommand(r)
		if err != nil || len(args) == 0 {
			return
		}
		name := strings.ToUpper(args[0])
		f.mu.Lock()
		f.seen = append(f.seen, name)
		f.lines = append(f.lines, strings.Join(append([]string{name}, args[1:]...), " "))
		info := f.info
		custom, hasCustom := f.replies[name]
		f.mu.Unlock()
		var reply string
		switch {
		case hasCustom:
			if name == "SUBSCRIBE" && len(args) == 2 && strings.HasPrefix(custom, "*") {
				channel = args[1]
			}
			reply = custom + "\r\n"
		case name == "PING":
			if channel == "" {
				reply = "+PONG\r\n"
			} else {
				payload := ""
				if len(args) > 1 {
					payload = args[1]
				}
				reply = fmt.Sprintf("*2\r\n$4\r\npong\r\n$%d\r\n%s\r\n", len(payload), payload)
			}
		case name == "SUBSCRIBE" && len(args) == 2:
			channel = args[1]
			reply = fmt.Sprintf("*3\r\n$9\r\nsubscribe\r\n$%d\r\n%s\r\n:1\r\n", len(channel), channel)
		case name == "PUBLISH":
			reply = ":0\r\n"
		case name == "EVAL" && fakeCountProbeReply(args) != "":
			// 只识别启动探测形状；业务 Lua 的原子行为由真实 Redis 测试验证。
			reply = fakeCountProbeReply(args)
		case name == "HDEL" || name == "ZREM":
			reply = ":0\r\n"
		case name == "AUTH" || name == "SELECT" || name == "SET":
			reply = "+OK\r\n"
		case name == "INFO":
			if info == "!drop" {
				return
			}
			if strings.HasPrefix(info, "-") {
				reply = info + "\r\n"
			} else {
				reply = fmt.Sprintf("$%d\r\n%s\r\n", len(info), info)
			}
		default:
			reply = "-ERR unknown command '" + args[0] + "'\r\n"
		}
		if _, err := io.WriteString(c, reply); err != nil {
			return
		}
	}
}

// fakeCountProbeReply 只接受固定计数或验证码键上的探测参数，不模拟业务脚本。
func fakeCountProbeReply(args []string) string {
	if len(args) == 10 && args[2] == "2" && args[5] == "probe" && args[6] == "" && args[7] == "" && args[8] == "0" &&
		strings.HasSuffix(args[3], "captcha:{captcha}:answers") && strings.HasSuffix(args[4], "captcha:{captcha}:expiry") {
		return ":1\r\n"
	}
	if len(args) < 6 || args[2] != "3" {
		return ""
	}
	if len(args) == 6 && strings.HasSuffix(args[3], "{counts}:windows") &&
		strings.HasSuffix(args[4], "{counts}:window-expiry") && strings.HasSuffix(args[5], "{counts}:window-capacity") {
		return ":1\r\n"
	}
	if len(args) == 8 && args[6] == "probe" && args[7] == "_probe" &&
		strings.HasSuffix(args[3], "guard:{counts}:data") && strings.HasSuffix(args[4], "guard:{counts}:expiry") &&
		strings.HasSuffix(args[5], "guard:{counts}:capacity") {
		return "*4\r\n:1\r\n:0\r\n:0\r\n:0\r\n"
	}
	return ""
}

// readTestCommand 读一条 RESP 数组形式的命令。
func readTestCommand(r *bufio.Reader) ([]string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "*")))
	if err != nil {
		return nil, err
	}
	args := make([]string, 0, n)
	for range n {
		head, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		size, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(head, "$")))
		if err != nil || size < 0 || size > 1<<20 {
			return nil, fmt.Errorf("bad bulk length %q", head)
		}
		buf := make([]byte, size+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		args = append(args, string(buf[:size]))
	}
	return args, nil
}
