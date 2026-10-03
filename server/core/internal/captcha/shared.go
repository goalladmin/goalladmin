package captcha

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/goalladmin/goalladmin/server/core/internal/redisx"
)

type sharedStore struct{ client *redisx.Client }

// NewShared 创建可选共享答案的生成器；没有 Redis 时仍按本实例存储。
func NewShared(now func() time.Time, client *redisx.Client) *Captcha {
	if now == nil {
		now = time.Now
	}
	c := newCaptcha(now, defaultTTL, defaultMax)
	if client != nil {
		c.shared = &sharedStore{client: client}
	}
	return c
}

func validID(id string, mode byte) bool {
	if len(id) != 1+2*idBytes || id[0] != mode {
		return false
	}
	for _, ch := range id[1:] {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

func (c *Captcha) store(ctx context.Context, scope, id, answer string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if c.shared != nil {
		if c.shared.client.Available() {
			now := c.now()
			var stored int64
			err := c.shared.client.Do(ctx, func(ctx context.Context, rdb redis.Cmdable) error {
				var err error
				stored, err = rdb.Eval(ctx, captchaScript, captchaKeys(c.shared.client.Key),
					"put", scope, "r"+id, now.UnixMilli(), c.ttl.Milliseconds(), c.max, answer).Int64()
				return err
			})
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return "", err
			}
			if err == nil && stored == 1 {
				return "r" + id, nil
			}
			// 原 ID 可能已写入共享存储，不能把同一个 ID 再放进本地。
			id, err = randomID()
			if err != nil {
				return "", err
			}
		}
		id = "l" + id
	}
	c.put(scope, id, answer)
	return id, nil
}

func (c *Captcha) consume(ctx context.Context, scope, id string) (string, bool) {
	if c.shared == nil {
		return c.take(scope, id)
	}
	if validID(id, 'r') {
		// 共享消费可能已经成功，只是回复丢失。任何错误都不能改走本地或重试。
		return c.shared.read(ctx, "take", scope, id, c.now())
	}
	if validID(id, 'l') {
		return c.take(scope, id)
	}
	return "", false
}

func captchaKeys(key func(...string) string) []string {
	return []string{key("captcha", "{captcha}", "answers"), key("captcha", "{captcha}", "expiry")}
}

func (s *sharedStore) read(ctx context.Context, op, scope, id string, now time.Time) (string, bool) {
	var answer string
	err := s.client.Do(ctx, func(ctx context.Context, rdb redis.Cmdable) error {
		var err error
		answer, err = rdb.Eval(ctx, captchaScript, captchaKeys(s.client.Key),
			op, scope, id, now.UnixMilli(), defaultTTL.Milliseconds()).Text()
		return err
	})
	return answer, err == nil && len(answer) == Length
}

// Probe 在固定答案容器上检查所有脚本命令，探测字段与验证码 ID 不重合。
func Probe(ctx context.Context, rdb redis.Cmdable, key func(...string) string) error {
	return rdb.Eval(ctx, captchaScript, captchaKeys(key), "probe", "", "", 0, defaultTTL.Milliseconds()).Err()
}

// 先检查全部实际权限，再操作用户记录；Redis 6 的脚本错误不回滚先前写入。
// 容器 TTL 只用于回收闲置数据；每条答案始终按应用传入的过期时间校验。
const captchaScript = `
local data, expiry = KEYS[1], KEYS[2]
local op, scope, id = ARGV[1], ARGV[2], ARGV[3]
local now, ttl = tonumber(ARGV[4]), tonumber(ARGV[5])
local probe = '_probe'
redis.call('HDEL', data, probe)
redis.call('ZREM', expiry, probe)
redis.call('HSET', data, probe, '{}')
redis.call('HGET', data, probe)
redis.call('HLEN', data)
redis.call('ZADD', expiry, 0, probe)
redis.call('ZRANGE', expiry, 0, 0)
redis.call('ZRANGEBYSCORE', expiry, 0, 0, 'LIMIT', 0, 1)
redis.call('PEXPIRE', data, ttl)
redis.call('PEXPIRE', expiry, ttl)
redis.call('HDEL', data, probe)
redis.call('ZREM', expiry, probe)
if op == 'probe' then return 1 end

local function remove(field)
    redis.call('HDEL', data, field)
    redis.call('ZREM', expiry, field)
end
if op == 'put' then
    if redis.call('HGET', data, id) then return 0 end
    local expired = redis.call('ZRANGEBYSCORE', expiry, '-inf', now, 'LIMIT', 0, 128)
    for _, field in ipairs(expired) do remove(field) end
    if redis.call('HLEN', data) >= tonumber(ARGV[6]) then
        local oldest = redis.call('ZRANGE', expiry, 0, 0)
        if #oldest == 0 then return 0 end
        remove(oldest[1])
    end
    redis.call('HSET', data, id, cjson.encode({s=scope, e=now+ttl, a=ARGV[7]}))
    redis.call('ZADD', expiry, now+ttl, id)
    redis.call('PEXPIRE', data, ttl)
    redis.call('PEXPIRE', expiry, ttl)
    return 1
end
local raw = redis.call('HGET', data, id)
if not raw then return false end
local value = cjson.decode(raw)
if value.e <= now then
    remove(id)
    return false
end
if op == 'peek' then return value.a end
if value.s ~= scope then return false end
remove(id)
return value.a
`
