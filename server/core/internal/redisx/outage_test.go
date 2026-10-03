package redisx

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// tlsFront 是测试里的 TLS 接入：对外讲 TLS，后面明文转给 target；证书可以随时换（换的时候断开已有的连接）。
type tlsFront struct {
	ln     net.Listener
	target string
	cert   atomic.Pointer[tls.Certificate]
	mu     sync.Mutex
	conns  map[net.Conn]struct{}
}

func newTLSFront(t *testing.T, target string, cert tls.Certificate) *tlsFront {
	t.Helper()
	f := &tlsFront{target: target, conns: map[net.Conn]struct{}{}}
	f.cert.Store(&cert)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return f.cert.Load(), nil },
	})
	require.NoError(t, err)
	f.ln = ln
	t.Cleanup(func() {
		_ = ln.Close()
		f.drop()
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.pipe(c)
		}
	}()
	return f
}

func (f *tlsFront) Addr() string { return f.ln.Addr().String() }

// use 换证书，并断开已有的连接。
func (f *tlsFront) use(cert tls.Certificate) {
	f.cert.Store(&cert)
	f.drop()
}

func (f *tlsFront) drop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for c := range f.conns {
		_ = c.Close()
	}
	f.conns = map[net.Conn]struct{}{}
}

func (f *tlsFront) pipe(c net.Conn) {
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	up, err := (&net.Dialer{}).DialContext(ctx, "tcp", f.target)
	if err != nil {
		return
	}
	defer func() { _ = up.Close() }()
	f.mu.Lock()
	f.conns[c], f.conns[up] = struct{}{}, struct{}{}
	f.mu.Unlock()
	go func() {
		_, _ = io.Copy(up, c)
		_ = up.Close()
	}()
	_, _ = io.Copy(c, up)
}

// testCA 造一个根证书和它签的、给 127.0.0.1 用的服务器证书。
func testCA(t *testing.T) (*x509.CertPool, tls.Certificate) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ga test ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	ca, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:    x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	require.NoError(t, err)
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return pool, tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// countLines 数日志里同时带这几个词的行。
func countLines(log string, words ...string) int {
	n := 0
	for _, line := range strings.Split(log, "\n") {
		all := true
		for _, w := range words {
			all = all && strings.Contains(line, w)
		}
		if all && line != "" {
			n++
		}
	}
	return n
}

func textLog(buf *syncBuffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// waitPings 等假服务器再收到 n 条 PING：探测又跑了至少 n 次。
func waitPings(t *testing.T, f *TestFakeServer, n int) {
	t.Helper()
	count := func() int {
		c := 0
		for _, name := range f.Commands() {
			if name == "PING" {
				c++
			}
		}
		return c
	}
	before := count()
	eventually(t, func() bool { return count() >= before+n }, "探测在跑")
}

// 规范 §13.2 第 163 条（D-074）：开了 TLS，服务器的证书验不过（过期、换了签发方）——建立连接这一步失败，
// 不管是不是网络错误都算故障：标成不可用、退回内存；证书好了自动恢复。启动时遇到也是按不可用处理，不拒绝启动。
func TestDo_163_TLSCertificateFailureIsAnOutage(t *testing.T) {
	f := NewTestFakeServer(t, "redis_version:7.2.4\r\n")
	trusted, good := testCA(t)
	_, other := testCA(t)
	front := newTLSFront(t, f.Addr(), good)
	open := func() *Client {
		c, err := Open(Options{
			Addr: front.Addr(), TLS: true, rootCAs: trusted, KeyPrefix: "t:", ProbeEvery: 10 * time.Millisecond, Log: quiet(),
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = c.Close() })
		return c
	}
	c := open()
	require.True(t, c.Available(), "证书验得过：TLS 连上了")
	require.NoError(t, set(c, "k", "v"))

	front.use(other)
	// 池里没有现成的连接（空闲太久被回收了）：下一次操作要新建连接，握手时证书验不过。
	// 不这样做的话，头一个错误是旧连接上的 EOF，看不出"建立连接失败"有没有被当成故障。
	old := c.rdb.Swap(redis.NewClient(c.ro))
	require.NoError(t, old.Close())
	err := set(c, "k", "v")
	require.ErrorIs(t, err, ErrUnavailable)
	require.ErrorContains(t, err, "certificate")
	require.False(t, c.Available())
	time.Sleep(80 * time.Millisecond)
	require.False(t, c.Available(), "探测也过不了")

	front.use(good)
	eventually(t, c.Available, "证书换回来之后恢复")
	require.NoError(t, set(c, "k", "v"))

	front.use(other)
	down := open()
	require.False(t, down.Available(), "启动时证书验不过：按不可用处理")
	front.use(good)
	eventually(t, down.Available, "证书好了之后恢复")
}

// 规范 §13.2 第 163 条（D-074）：调用方自己的时限到了（请求快超时了才来问 Redis）不算 Redis 的问题——
// 哪怕读超时比 ctx 的定时器先返回、那一刻 ctx.Err() 还是空的。
func TestDo_163_CallerDeadlineIsNotAnOutage(t *testing.T) {
	p := NewTestProxy(t, TestAddr(t))
	c := OpenTest(t, p)
	for i := range 40 {
		require.NoError(t, set(c, "k", "v")) // 池里有一条好的连接
		p.Freeze()                           // 这条连接从此收不到回应
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
		err := c.Do(ctx, func(ctx context.Context, rdb redis.Cmdable) error {
			return rdb.Get(ctx, c.Key("k")).Err()
		})
		cancel()
		require.Error(t, err, i)
		require.NotErrorIs(t, err, ErrUnavailable, i)
		require.True(t, c.Available(), i)
	}
}

// 规范 §13.2 第 163 条（D-074）：出过故障、换了连接池、已经恢复之后，旧池子上一次迟到的错误不能把新的标成不可用。
func TestDo_163_LateErrorFromReplacedPoolDoesNotMarkDown(t *testing.T) {
	p := NewTestProxy(t, TestAddr(t))
	c := OpenTest(t, p)
	started, release, result := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		result <- c.Do(context.Background(), func(context.Context, redis.Cmdable) error {
			close(started)
			<-release
			return io.ErrUnexpectedEOF
		})
	}()
	<-started
	p.Cut()
	require.ErrorIs(t, set(c, "k", "v"), ErrUnavailable)
	p.Restore()
	eventually(t, c.Available, "恢复")

	close(release)
	require.ErrorIs(t, <-result, ErrUnavailable, "这一次操作仍然是失败的")
	require.True(t, c.Available(), "但不影响新的连接池")
	require.NoError(t, set(c, "k", "v"))

	// 对照：没换过池子时，同样的错误就是故障
	err := c.Do(context.Background(), func(context.Context, redis.Cmdable) error { return io.ErrUnexpectedEOF })
	require.ErrorIs(t, err, ErrUnavailable)
	require.False(t, c.Available())
}

// 规范 §13.2 第 163 条（D-074）：探测的日志——同一个原因在一次故障期间只记一次（两个原因来回换也是各记一次），
// 原因种类太多时到上限为止；恢复之后再出故障重新记。
func TestProbe_163_EachReasonLoggedOncePerOutage(t *testing.T) {
	const (
		loading = "-LOADING Redis is loading the dataset in memory"
		misconf = "-MISCONF Redis is configured to save RDB snapshots"
		still   = "redis still unavailable"
	)
	f := NewTestFakeServer(t, "redis_version:7.2.4\r\n")
	var buf syncBuffer
	c, err := openFake(t, f, Options{Log: textLog(&buf)})
	require.NoError(t, err)
	fail := func() {
		f.SetReply("GET", loading)
		f.SetReply("PING", loading)
		_, err := get(c, "k")
		require.ErrorIs(t, err, ErrUnavailable)
	}
	fail()
	for range 4 {
		f.SetReply("PING", loading)
		waitPings(t, f, 2)
		f.SetReply("PING", misconf)
		waitPings(t, f, 2)
	}
	require.Equal(t, 1, countLines(buf.String(), still, "LOADING"), buf.String())
	require.Equal(t, 1, countLines(buf.String(), still, "MISCONF"), buf.String())

	f.SetReply("GET", "")
	f.SetReply("PING", "")
	eventually(t, c.Available, "恢复")
	fail()
	eventually(t, func() bool { return countLines(buf.String(), still, "LOADING") == 2 }, "新的一次故障重新记")

	// 原因五花八门：最多记 maxReasons 种
	f = NewTestFakeServer(t, "redis_version:7.2.4\r\n")
	var many syncBuffer
	c, err = openFake(t, f, Options{Log: textLog(&many)})
	require.NoError(t, err)
	fail()
	for i := range 12 {
		f.SetReply("PING", fmt.Sprintf("-LOADING reason-%d", i))
		waitPings(t, f, 2)
	}
	require.Equal(t, 8, countLines(many.String(), still), many.String())
}

// 同一种网络错误每次带的本机临时端口不一样：算同一个原因。
func TestReason(t *testing.T) {
	op := func(port int) error {
		return &net.OpError{
			Op: "read", Net: "tcp",
			Source: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: port},
			Addr:   &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 6379},
			Err:    errors.New("connection reset by peer"),
		}
	}
	require.NotEqual(t, op(48252).Error(), op(48253).Error())
	require.Equal(t, reason(op(48252)), reason(op(48253)))
	require.Equal(t, "read tcp: connection reset by peer", reason(op(48252)))
	require.Equal(t, reason(op(1)), reason(fmt.Errorf("redis: %w", &dialError{err: op(2)})))
	require.Equal(t, "boom", reason(errors.New("boom")))
	require.NotEqual(t, reason(op(1)), reason(errors.New("boom")))
}

// Redis 不可用的时候关闭：探测是被关闭打断的，不当成新的故障原因记日志。
func TestClose_163_NoOutageLogFromShutdown(t *testing.T) {
	p := NewTestProxy(t, TestAddr(t))
	p.Cut()
	for range 60 {
		var buf syncBuffer
		c, err := Open(Options{Addr: p.Addr(), KeyPrefix: "t:", ProbeEvery: time.Millisecond, Log: textLog(&buf)})
		require.NoError(t, err)
		time.Sleep(5 * time.Millisecond)
		require.NoError(t, c.Close())
		require.NotContains(t, buf.String(), "cancel", buf.String())
	}
}

// 规范 §13.2 第 163 条（D-074）：6.0 之前的服务器不认带用户名的登录——也是拒绝启动，并说明多半是版本太低。
func TestOpen_163_UsernameAgainstPre6Server(t *testing.T) {
	for _, reply := range []string{
		"-ERR wrong number of arguments for 'auth' command",
		"-ERR wrong number of arguments for 'AUTH' command",
	} {
		f := NewTestFakeServer(t, "redis_version:5.0.14\r\n")
		f.SetReply("AUTH", reply)
		c, err := openFake(t, f, Options{Username: "app", Password: "s3cret-redis-pass", ConnectWait: time.Second})
		var rejected *RejectedError
		require.ErrorAs(t, err, &rejected, reply)
		require.Nil(t, c)
		require.Contains(t, err.Error(), "6.0")
		require.Contains(t, err.Error(), "wrong number of arguments")
		require.NotContains(t, err.Error(), "s3cret-redis-pass")
	}
	// 别的拒绝不带这句提示
	f := NewTestFakeServer(t, "redis_version:7.2.4\r\n")
	f.SetReply("AUTH", "-WRONGPASS invalid username-password pair or user is disabled.")
	_, err := openFake(t, f, Options{Username: "app", Password: "s3cret-redis-pass"})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "6.0")
}

// 规范 §13.2 第 163 条（D-074）：接入层把命令的参数回显在错误里时，密码从日志和返回的错误里抹掉；
// 抹掉之后错误的分类不变。
func TestSafeErr_163_PasswordEchoedByServerIsRedacted(t *testing.T) {
	const (
		pass = "s3cret-redis-pass"
		info = "redis_version:7.2.4\r\n"
	)
	// 启动时连不上那一类：记日志的地方
	f := NewTestFakeServer(t, info)
	f.SetReply("AUTH", "-ERR unknown command `AUTH`, with args beginning with: `"+pass+"`, ")
	var buf syncBuffer
	c, err := openFake(t, f, Options{Password: pass, Log: textLog(&buf)})
	require.NoError(t, err)
	require.False(t, c.Available())
	eventually(t, func() bool { return strings.Contains(buf.String(), "redis still unavailable") }, "探测")
	require.Equal(t, 1, countLines(buf.String(), "redis unavailable at startup", "unknown command", "***"), buf.String())
	require.Equal(t, 1, countLines(buf.String(), "redis still unavailable", "unknown command", "***"), buf.String())
	require.NotContains(t, buf.String(), pass)

	// 启动时被拒绝：返回的错误
	f = NewTestFakeServer(t, info)
	f.SetReply("AUTH", "-WRONGPASS no such pair: "+pass)
	_, err = openFake(t, f, Options{Password: pass})
	var rejected *RejectedError
	require.ErrorAs(t, err, &rejected)
	require.Contains(t, err.Error(), "WRONGPASS no such pair: ***")
	require.NotContains(t, err.Error(), pass)

	// 读版本被拒绝的那条警告
	f = NewTestFakeServer(t, "-ERR unknown command `INFO`, near "+pass)
	var warn syncBuffer
	c, err = openFake(t, f, Options{Password: pass, Log: textLog(&warn)})
	require.NoError(t, err)
	require.True(t, c.Available())
	require.Equal(t, 1, countLines(warn.String(), "cannot read redis server version", "***"), warn.String())
	require.NotContains(t, warn.String(), pass)

	// 运行中：普通的错误回复原样返回（抹掉密码），故障的那一类进日志、包着 ErrUnavailable
	f = NewTestFakeServer(t, info)
	var run syncBuffer
	c, err = openFake(t, f, Options{Password: pass, Log: textLog(&run)})
	require.NoError(t, err)
	f.SetReply("GET", "-ERR echo "+pass+" back")
	_, err = get(c, "k")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrUnavailable)
	require.Equal(t, "ERR echo *** back", err.Error())
	var reply redis.Error
	require.ErrorAs(t, err, &reply, "仍然认得出是服务器回的错误")
	require.True(t, c.Available())

	f.SetReply("GET", "-NOAUTH "+pass)
	_, err = get(c, "k")
	require.ErrorIs(t, err, ErrUnavailable)
	require.Contains(t, err.Error(), "NOAUTH ***")
	require.NotContains(t, err.Error(), pass)
	require.Equal(t, 1, countLines(run.String(), "falling back to in-process state", "NOAUTH ***"), run.String())
	require.NotContains(t, run.String(), pass)

	// 没设密码、错误里没有密码、不是服务器回的错误：不动
	plain := &Client{}
	e := errors.New("boom")
	require.Same(t, e, plain.safeErr(e))
	withPass := &Client{secret: "tcp"}
	netErr := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("refused")}
	require.Same(t, error(netErr), withPass.safeErr(netErr), "只处理服务器回的错误，不去改别的错误的文字")
	require.NoError(t, withPass.safeErr(nil))
}
