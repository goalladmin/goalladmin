package redisx

import (
	"context"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type testListener struct {
	messages chan string
	ready    atomic.Int64
	cancel   context.CancelFunc
	done     chan struct{}
}

func listenTest(t *testing.T, c *Client) *testListener {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	l := &testListener{messages: make(chan string, 32), cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(l.done)
		c.Listen(ctx, func(payload string) { l.messages <- payload }, func() { l.ready.Add(1) })
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-l.done:
		case <-time.After(time.Second):
			t.Error("订阅取消后没有退出")
		}
	})
	eventually(t, func() bool { return l.ready.Load() == 1 }, "订阅确认")
	return l
}

func receiveTest(t *testing.T, l *testListener, want string) {
	t.Helper()
	select {
	case got := <-l.messages:
		require.Equal(t, want, got)
	case <-time.After(time.Second):
		t.Fatalf("未收到消息 %q", want)
	}
}

func openPubSubTest(t *testing.T, addr, prefix string, every time.Duration) *Client {
	t.Helper()
	c, err := Open(Options{Addr: addr, KeyPrefix: prefix, Log: quiet(), ProbeEvery: every})
	require.NoError(t, err)
	require.True(t, c.Available())
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// 规范 §13.2 第 164 条（D-075）：两个实例订阅相同的部署通道；不同前缀收不到，探测空消息不交给接收方。
func TestPubSub_164_RealRedisAndPrefixIsolation(t *testing.T) {
	addr, prefix := TestAddr(t), TestPrefix(t)
	a := openPubSubTest(t, addr, prefix, 20*time.Millisecond)
	b := openPubSubTest(t, addr, prefix, 20*time.Millisecond)
	other := openPubSubTest(t, addr, TestPrefix(t), 20*time.Millisecond)
	la, lb, lo := listenTest(t, a), listenTest(t, b), listenTest(t, other)
	require.NoError(t, a.Publish(context.Background(), ""))
	require.NoError(t, a.Publish(context.Background(), "change"))
	receiveTest(t, la, "change")
	receiveTest(t, lb, "change")
	select {
	case got := <-lo.messages:
		t.Fatalf("不同部署收到了消息 %q", got)
	case <-time.After(30 * time.Millisecond):
	}
}

// 等心跳回复期间到达的通知照常交付，不能为了等 PONG 把已发布的消息吃掉。
func TestPubSub_164_MessageBeforeHeartbeatReply(t *testing.T) {
	c := OpenTest(t, nil)
	sub := c.rdb.Load().Subscribe(context.Background())
	defer func() { _ = sub.Close() }()
	require.NoError(t, c.subscribe(context.Background(), sub))
	require.NoError(t, c.Publish(context.Background(), "before-pong"))
	var got string
	require.NoError(t, c.subscriptionPing(context.Background(), sub, func(payload string) { got = payload }, nil))
	require.Equal(t, "before-pong", got)
}

// 空闲的接收超时只用来发心跳，不是故障，也不应反复重订阅。
func TestPubSub_164_IdleConnectionStaysAvailable(t *testing.T) {
	f := NewTestFakeServer(t, "redis_version:6.0.0\r\n")
	c, err := openFake(t, f, Options{})
	require.NoError(t, err)
	l := listenTest(t, c)
	before := len(f.Commands())
	time.Sleep(defaultProbeEvery + 100*time.Millisecond)
	require.True(t, c.Available())
	require.EqualValues(t, 1, l.ready.Load())
	require.Contains(t, f.Commands()[before:], "PING", "空闲订阅会发心跳")
	require.NotContains(t, f.Commands()[before:], "SUBSCRIBE", "普通空闲超时不重连")
}

// 接收循环在初次确认后又收到新的确认（客户端库自行重连的情形），必须再次清缓存。
func TestPubSub_164_ResubscribeAcknowledgmentWhileReceiving(t *testing.T) {
	f := NewTestFakeServer(t, "redis_version:6.0.0\r\n")
	ack := "*3\r\n$9\r\nsubscribe\r\n$12\r\nt:invalidate\r\n:1"
	f.SetReply("SUBSCRIBE", ack+"\r\n"+ack)
	c, err := openFake(t, f, Options{})
	require.NoError(t, err)
	var resets atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Listen(ctx, nil, func() { resets.Add(1) })
	}()
	eventually(t, func() bool { return resets.Load() == 2 }, "接收路径的再次确认清缓存")
	require.True(t, c.Available())
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("取消未停止订阅")
	}
}

// 等心跳期间收到客户端库重订阅的确认，也必须清缓存，再继续等 PONG。
func TestPubSub_164_ResubscribeAcknowledgmentWhilePinging(t *testing.T) {
	f := NewTestFakeServer(t, "redis_version:6.0.0\r\n")
	c, err := openFake(t, f, Options{})
	require.NoError(t, err)
	l := listenTest(t, c)
	f.SetReply("PING", "*3\r\n$9\r\nsubscribe\r\n$12\r\nt:invalidate\r\n:1\r\n*2\r\n$4\r\npong\r\n$5\r\nalive")
	eventually(t, func() bool { return l.ready.Load() == 2 }, "心跳路径的再次确认清缓存")
	require.True(t, c.Available())
	f.SetReply("PING", "")
}

// 不需要业务 Do 来发现订阅故障；故障时不继续拨号，恢复后清缓存回调和订阅确认都执行，通知继续送达。
func TestPubSub_164_DisconnectAndRecovery(t *testing.T) {
	p := NewTestProxy(t, TestAddr(t))
	c := openPubSubTest(t, p.Addr(), TestPrefix(t), 20*time.Millisecond)
	var ups atomic.Int64
	c.OnUp(func() { ups.Add(1) })
	l := listenTest(t, c)
	p.Cut()
	eventually(t, func() bool { return !c.Available() }, "订阅单独报告故障")
	require.ErrorIs(t, c.Publish(context.Background(), "down"), ErrUnavailable)
	p.Restore()
	eventually(t, func() bool { return ups.Load() == 1 && l.ready.Load() == 2 }, "恢复回调后重新订阅")
	require.NoError(t, c.Publish(context.Background(), "restored"))
	receiveTest(t, l, "restored")
}

// 连接不断开但不再回应时，心跳会发现假死；恢复只认新连接池，旧池上迟到的错误无效。
func TestPubSub_164_HalfOpenAndLateFailure(t *testing.T) {
	p := NewTestProxy(t, TestAddr(t))
	c := openPubSubTest(t, p.Addr(), TestPrefix(t), 100*time.Millisecond)
	l := listenTest(t, c)
	old := c.rdb.Load()
	p.Freeze()
	eventually(t, func() bool { return !c.Available() }, "心跳发现半开连接")
	eventually(t, func() bool { return c.Available() && l.ready.Load() == 2 }, "换池并重新订阅")
	c.markDown(old, io.ErrUnexpectedEOF)
	require.True(t, c.Available(), "旧连接的迟到错误不影响新池")
	require.NoError(t, c.Publish(context.Background(), "fresh"))
	receiveTest(t, l, "fresh")
}

// 没有可用的池时，订阅等待探测，不发自己的重连请求。
func TestPubSub_164_DownListenerDoesNotDial(t *testing.T) {
	f := NewTestFakeServer(t, "redis_version:6.0.0\r\n")
	p := NewTestProxy(t, f.Addr())
	c := openPubSubTest(t, p.Addr(), "t:", time.Hour)
	l := listenTest(t, c)
	p.Cut()
	eventually(t, func() bool { return !c.Available() }, "订阅故障")
	before := p.Accepts()
	time.Sleep(60 * time.Millisecond)
	require.Equal(t, before, p.Accepts(), "故障期间订阅不拨号")
	require.EqualValues(t, 1, l.ready.Load())
}

// 调用方取消和 Client.Close 都会关闭专用订阅连接，取消不触发 Redis 故障。
func TestPubSub_164_CancelAndClose(t *testing.T) {
	c := OpenTest(t, nil)
	l := listenTest(t, c)
	l.cancel()
	select {
	case <-l.done:
	case <-time.After(time.Second):
		t.Fatal("调用方取消未停止订阅")
	}
	require.True(t, c.Available())
	l2 := listenTest(t, c)
	require.NoError(t, c.Close())
	select {
	case <-l2.done:
	case <-time.After(time.Second):
		t.Fatal("关闭客户端未停止订阅")
	}
}

// 发布订阅权限在启动时检查，而且检查真实通道，不能用一个权限更宽的探测通道代替。
func TestPubSub_164_RequiredPermissionsAndPasswordRedaction(t *testing.T) {
	for _, command := range []string{"SUBSCRIBE", "PUBLISH"} {
		t.Run(command, func(t *testing.T) {
			f := NewTestFakeServer(t, "redis_version:6.0.0\r\n")
			const secret = "subscription-secret"
			f.SetReply(command, "-NOPERM permission denied "+secret)
			c, err := openFake(t, f, Options{Password: secret})
			var rejected *RejectedError
			require.ErrorAs(t, err, &rejected)
			require.Nil(t, c)
			require.NotContains(t, err.Error(), secret)
			require.Contains(t, err.Error(), "***")
			found := false
			for _, line := range f.Lines() {
				if strings.HasPrefix(line, command+" ") {
					require.True(t, strings.HasPrefix(line, command+" t:invalidate"), line)
					found = true
				}
			}
			require.True(t, found)
		})
	}
}

// 运行中订阅权限失效，同样走故障状态机；探测也被拒，不会反复标回可用。
func TestPubSub_164_RuntimePermissionFailureStaysDown(t *testing.T) {
	f := NewTestFakeServer(t, "redis_version:6.0.0\r\n")
	c, err := openFake(t, f, Options{})
	require.NoError(t, err)
	l := listenTest(t, c)
	f.SetReply("PING", "-NOPERM subscription ping denied")
	eventually(t, func() bool { return !c.Available() }, "订阅心跳权限失效")
	time.Sleep(40 * time.Millisecond)
	require.False(t, c.Available())
	require.EqualValues(t, 1, l.ready.Load())
	f.SetReply("PING", "")
	eventually(t, func() bool { return c.Available() && l.ready.Load() == 2 }, "权限恢复后重新订阅")
}

// 发布被拒后不再发送业务命令，后台探测也必须检查发布权限；服务端回显的密码不能进错误和日志。
func TestPubSub_164_PublicationFailureAndRedaction(t *testing.T) {
	f := NewTestFakeServer(t, "redis_version:6.0.0\r\n")
	var buf syncBuffer
	const secret = "publication-secret"
	c, err := openFake(t, f, Options{Password: secret, Log: textLog(&buf)})
	require.NoError(t, err)
	f.SetReply("PUBLISH", "-NOPERM publish denied "+secret)
	err = c.Publish(context.Background(), "change")
	require.ErrorIs(t, err, ErrUnavailable)
	require.NotContains(t, err.Error(), secret)
	require.False(t, c.Available())
	eventually(t, func() bool { return strings.Contains(buf.String(), "redis still unavailable") }, "探测仍缺发布权限")
	require.False(t, c.Available())
	require.NotContains(t, buf.String(), secret)
	require.Contains(t, buf.String(), "***")
	f.SetReply("PUBLISH", "")
	eventually(t, c.Available, "发布权限恢复")
	require.NoError(t, c.Publish(context.Background(), "restored"))
}
