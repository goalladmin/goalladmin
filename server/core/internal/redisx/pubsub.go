package redisx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/redis/go-redis/v9"
)

// Publish 向本部署的固定通道发布缓存失效消息（D-075）。和其他短操作一样走 Do，有时限、不重试。
func (c *Client) Publish(ctx context.Context, payload string) error {
	return c.Do(ctx, func(ctx context.Context, rdb redis.Cmdable) error {
		return rdb.Publish(ctx, c.invalidationChannel(), payload).Err()
	})
}

func (c *Client) invalidationChannel() string { return c.Key("invalidate") }

// Listen 接收本部署的失效通知，直到 ctx 取消或 Client 关闭。调用方在自己的协程里调用它。
// receive 只清内存，不能读库或发布；subscribed 在每次订阅得到确认后清缓存，覆盖通知漏收的窗口。
// 长连接的接收不走 Do：空闲不算故障，空闲读超时后发送有时限的 PING，断线和假死报告给同一个故障状态机。
func (c *Client) Listen(ctx context.Context, receive func(string), subscribed func()) {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.base, cancel)
	defer stop()
	defer cancel()
	for ctx.Err() == nil {
		if c.up.Load() {
			rdb := c.rdb.Load()
			err := c.listenConnection(ctx, rdb, receive, subscribed)
			if ctx.Err() != nil {
				return
			}
			if err != nil && (isOutage(err) || errors.Is(err, redis.ErrClosed)) {
				c.markDown(rdb, err)
			}
		}
		// 不可用时只等后台探测，不自行重连；其他错误同样等一轮，避免忙循环。
		timer := time.NewTimer(c.probeEvery)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (c *Client) listenConnection(ctx context.Context, rdb *redis.Client, receive func(string), subscribed func()) error {
	sub := rdb.Subscribe(ctx)
	stop := context.AfterFunc(ctx, func() { _ = sub.Close() })
	defer stop()
	defer func() { _ = sub.Close() }()
	if err := c.subscribe(ctx, sub); err != nil {
		return err
	}
	if subscribed != nil {
		c.runHook(subscribed)
	}
	for ctx.Err() == nil && c.up.Load() && c.rdb.Load() == rdb {
		msg, err := sub.ReceiveTimeout(ctx, defaultProbeEvery)
		if err != nil {
			var ne net.Error
			if ctx.Err() == nil && errors.As(err, &ne) && ne.Timeout() {
				if err := c.subscriptionPing(ctx, sub, receive, subscribed); err != nil {
					return err
				}
				continue
			}
			return err
		}
		c.deliver(msg, receive, subscribed)
	}
	return nil
}

func (c *Client) subscribe(ctx context.Context, sub *redis.PubSub) error {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	channel := c.invalidationChannel()
	if err := sub.Subscribe(ctx, channel); err != nil {
		return err
	}
	msg, err := sub.ReceiveTimeout(ctx, ioTimeout)
	if err != nil {
		return err
	}
	ack, ok := msg.(*redis.Subscription)
	if !ok || ack.Kind != "subscribe" || ack.Channel != channel || ack.Count != 1 {
		return fmt.Errorf("redisx: 未收到订阅确认")
	}
	return nil
}

// subscriptionPing 必须收到订阅连接上的 PONG；收到消息也照常交给接收方，不能把这一段通知吃掉。
func (c *Client) subscriptionPing(ctx context.Context, sub *redis.PubSub, receive func(string), subscribed func()) error {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	if err := sub.Ping(ctx, "alive"); err != nil {
		return err
	}
	for {
		msg, err := sub.ReceiveTimeout(ctx, ioTimeout)
		if err != nil {
			return err
		}
		if pong, ok := msg.(*redis.Pong); ok && pong.Payload == "alive" {
			return nil
		}
		c.deliver(msg, receive, subscribed)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

func (c *Client) deliver(msg any, receive func(string), subscribed func()) {
	switch message := msg.(type) {
	case *redis.Message:
		if message.Channel == c.invalidationChannel() && message.Payload != "" && receive != nil {
			c.runHook(func() { receive(message.Payload) })
		}
	case *redis.Subscription:
		// 客户端库可能在接收期间自行重建连接；新的确认同样要清缓存，补上重连期间漏收的通知。
		if message.Kind == "subscribe" && message.Channel == c.invalidationChannel() && message.Count == 1 && subscribed != nil {
			c.runHook(subscribed)
		}
	}
}

// checkSubscription 用实际通道探测发布、订阅和订阅 PING 权限。空消息没有失效含义，接收方会忽略。
func (c *Client) checkSubscription(ctx context.Context, rdb *redis.Client) error {
	sub := rdb.Subscribe(ctx)
	defer func() { _ = sub.Close() }()
	if err := c.subscribe(ctx, sub); err != nil {
		return err
	}
	if err := c.subscriptionPing(ctx, sub, nil, nil); err != nil {
		return err
	}
	return rdb.Publish(ctx, c.invalidationChannel(), "").Err()
}
