package captcha

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/internal/redisx"
)

// 真实 Redis 执行命令并返回后才丢弃一次回复；用来验证无法判断是否已写入/消费的情况。
type replyLoss struct {
	addr    string
	armed   atomic.Bool
	dropped atomic.Int32
}

func loseCaptchaReply(t *testing.T, target, op string) *replyLoss {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	p := &replyLoss{addr: ln.Addr().String()}
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			down, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Go(func() {
				defer func() { _ = down.Close() }()
				up, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", target)
				if err != nil {
					return
				}
				defer func() { _ = up.Close() }()
				stop := context.AfterFunc(ctx, func() { _ = down.Close(); _ = up.Close() })
				defer stop()
				var drop atomic.Bool
				done := make(chan struct{}, 2)
				go func() {
					defer func() { done <- struct{}{} }()
					r := bufio.NewReader(down)
					marker := []byte(fmt.Sprintf("\r\n$%d\r\n%s\r\n", len(op), op))
					for {
						frame, err := readTestFrame(r)
						if err != nil {
							return
						}
						if bytes.Contains(frame, []byte(captchaScript)) && bytes.Contains(frame, marker) && p.armed.CompareAndSwap(true, false) {
							drop.Store(true)
						}
						if _, err := up.Write(frame); err != nil {
							return
						}
					}
				}()
				go func() {
					defer func() { done <- struct{}{} }()
					r := bufio.NewReader(up)
					for {
						frame, err := readTestFrame(r)
						if err != nil {
							return
						}
						if drop.Swap(false) {
							p.dropped.Add(1)
							return
						}
						if _, err := down.Write(frame); err != nil {
							return
						}
					}
				}()
				<-done
				_ = up.Close()
				_ = down.Close()
				<-done
			})
		}
	})
	t.Cleanup(func() { cancel(); _ = ln.Close(); wg.Wait() })
	return p
}

func readTestFrame(r *bufio.Reader) ([]byte, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if len(line) < 3 {
		return nil, fmt.Errorf("short RESP frame")
	}
	out := []byte(line)
	switch line[0] {
	case '+', '-', ':':
		return out, nil
	case '$', '*':
		n, err := strconv.Atoi(strings.TrimSpace(line[1:]))
		if err != nil || n < -1 || n > 1<<20 {
			return nil, fmt.Errorf("invalid RESP length")
		}
		if n < 0 {
			return out, nil
		}
		if line[0] == '$' {
			body := make([]byte, n+2)
			_, err := io.ReadFull(r, body)
			return append(out, body...), err
		}
		for range n {
			child, err := readTestFrame(r)
			if err != nil {
				return nil, err
			}
			out = append(out, child...)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unexpected RESP type")
	}
}

func TestShared_166_LostWriteReplyUsesNewLocalID(t *testing.T) {
	addr, prefix := redisx.TestAddr(t), redisx.TestPrefix(t)
	p := loseCaptchaReply(t, addr, "put")
	a := NewShared(nil, openCaptchaClient(t, p.addr, prefix, ""))
	b := NewShared(nil, openCaptchaClient(t, addr, prefix, ""))
	p.armed.Store(true)
	id, answer := generateShared(t, a, "platform")
	require.True(t, validID(id, 'l'))
	require.EqualValues(t, 1, p.dropped.Load())
	raw := rawCaptchaClient(t, "")
	fields, err := raw.HKeys(context.Background(), captchaKeys(a.shared.client.Key)[0]).Result()
	require.NoError(t, err)
	require.Len(t, fields, 1, "真实 Redis 已经写入，只丢了回复；没有重试")
	require.True(t, validID(fields[0], 'r'))
	require.NotEqual(t, fields[0][1:], id[1:], "回退必须换一个随机 ID")
	require.False(t, b.VerifyFor(context.Background(), "platform", id, answer))
	require.True(t, a.VerifyFor(context.Background(), "platform", id, answer))
	require.Eventually(t, func() bool { return a.shared.client.Available() }, 3*time.Second, 10*time.Millisecond)
	require.False(t, a.VerifyFor(context.Background(), "platform", id, answer))
}

func TestShared_166_LostConsumeReplyCannotReplay(t *testing.T) {
	addr, prefix := redisx.TestAddr(t), redisx.TestPrefix(t)
	p := loseCaptchaReply(t, addr, "take")
	a := NewShared(nil, openCaptchaClient(t, p.addr, prefix, ""))
	b := NewShared(nil, openCaptchaClient(t, addr, prefix, ""))
	id, answer := generateShared(t, a, "platform")
	p.armed.Store(true)
	require.False(t, a.VerifyFor(context.Background(), "platform", id, answer))
	require.EqualValues(t, 1, p.dropped.Load())
	require.Zero(t, a.size())
	require.Eventually(t, func() bool { return a.shared.client.Available() }, 3*time.Second, 10*time.Millisecond)
	require.Empty(t, b.Peek(id), "真实 Redis 已消费，只丢了回复")
	require.False(t, a.VerifyFor(context.Background(), "platform", id, answer))
	require.False(t, b.VerifyFor(context.Background(), "platform", id, answer))
}
