package ratelimit

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"github.com/goalladmin/goalladmin/server/core/internal/redisx"
	"github.com/redis/go-redis/v9"
)

// 共享预算按用途最多十万；测试或调用方可以选更小的预算。
const maxSharedWindowKeys = 100_000

// 三个键按部署固定，namespace 和用户维度放在散列字段里，ACL 无需放开用户生成的键。
func windowKeys(key func(...string) string) []string {
	return []string{key("{counts}", "windows"), key("{counts}", "window-expiry"), key("{counts}", "window-capacity")}
}

func digest(s string) string {
	v := sha256.Sum256([]byte(s))
	return hex.EncodeToString(v[:])
}

func generation() (string, error) {
	var v [16]byte
	if _, err := rand.Read(v[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(v[:]), nil
}

type sharedWindow struct {
	client    *redisx.Client
	namespace string
	keys      []string
}

// NewShared 创建可选的共享窗口。Redis 健康时使用共享准入结果，并维护本地影子；
// Redis 不可用时直接使用影子。nil client 与 New 相同。
func NewShared(limit int, span time.Duration, maxKeys int, now func() time.Time, client *redisx.Client, namespace string) *Window {
	if client != nil {
		maxKeys = min(maxKeys, maxSharedWindowKeys)
	}
	w := New(limit, span, maxKeys, now)
	if client != nil {
		w.shared = &sharedWindow{client: client, namespace: digest(namespace), keys: windowKeys(client.Key)}
	}
	return w
}

// Reservation 绑定实际占用的后端和窗口代数。Undo 与 Reset 合计只收尾一次，
// 因而可以在成功时 Reset，同时用 defer Undo 兜底。
type Reservation struct {
	once           sync.Once
	window         *Window
	key            string
	entry          *entry
	localIncrement bool
	localPending   bool
	shared         bool
	field          string
	generation     string
}

// Reserve 先为请求占用一次机会。取消的请求不占用；成功时收尾必须使用这份凭据。
func (w *Window) Reserve(ctx context.Context, key string) (*Reservation, bool) {
	if windowRequestDone(ctx) {
		return nil, false
	}
	healthy := w.shared != nil && w.shared.client.Available()
	w.mu.Lock()
	if windowRequestDone(ctx) {
		w.mu.Unlock()
		return nil, false
	}
	now := w.now()
	e, incremented, ok := w.reserveLocal(key, now, healthy)
	w.mu.Unlock()
	if !ok {
		return nil, false
	}
	r := &Reservation{window: w, key: key, entry: e, localIncrement: incremented, localPending: healthy}
	if windowRequestDone(ctx) {
		r.releaseLocal(false)
		return nil, false
	}
	if !healthy {
		return r, true
	}
	gen, err := generation()
	if err == nil {
		r.field = w.shared.namespace + ":" + digest(key)
		var result []interface{}
		err = w.shared.client.Do(ctx, func(opCtx context.Context, rdb redis.Cmdable) error {
			var callErr error
			result, callErr = rdb.Eval(opCtx, reserveWindowScript, w.shared.keys,
				r.field, w.shared.namespace, now.UnixMilli(), max(int64(1), w.window.Milliseconds()),
				w.limit, w.maxKeys, gen, windowGC).Slice()
			return callErr
		})
		if err == nil && len(result) == 2 {
			allowed, valid := result[0].(int64)
			if valid && allowed == 0 {
				r.releaseLocal(false)
				return nil, false
			}
			if actual, hasGeneration := result[1].(string); valid && allowed == 1 && hasGeneration && actual != "" {
				r.shared, r.generation = true, actual
				if windowRequestDone(ctx) {
					r.Undo()
					return nil, false
				}
				r.confirmLocal(true)
				return r, true
			}
		}
	}
	// 结果不确定时不重试。共享配额可能已经消耗，只使用原先保留的合法本地占用。
	if windowRequestDone(ctx) {
		r.releaseLocal(false)
		return nil, false
	}
	if !r.confirmLocal(false) {
		return nil, false
	}
	return r, true
}

// 连接读写时限可能先于 context 的取消定时器触发，仍须按调用方时限拒绝。
// 请求时限使用真实时间，不使用计数窗口的可注入时钟。
func windowRequestDone(ctx context.Context) bool {
	if ctx.Err() != nil {
		return true
	}
	deadline, ok := ctx.Deadline()
	return ok && !time.Now().Before(deadline)
}

// reserveLocal 在健康时允许影子计数饱和：共享窗口比本地更早起算，
// 共享窗口过期后不能被较晚的影子窗口误挡。断网时仍按原 limit 拒绝。
func (w *Window) reserveLocal(key string, now time.Time, healthy bool) (*entry, bool, bool) {
	e, exists := w.m[key]
	if exists && now.Sub(e.start) >= w.window {
		w.removeLocal(key, e)
		exists = false
	}
	if !exists {
		if len(w.m) >= w.maxKeys {
			w.gcLocked(now)
			if len(w.m) >= w.maxKeys {
				return nil, false, false
			}
		}
		e = &entry{start: now, index: -1}
		w.m[key] = e
		w.trackLocal(key, e)
	}
	if healthy {
		e.pending++
		return e, false, true
	}
	if e.n+e.pending >= w.limit {
		return nil, false, false
	}
	e.n++
	return e, true, true
}

func (r *Reservation) releaseLocal(reset bool) {
	w := r.window
	w.mu.Lock()
	defer w.mu.Unlock()
	if r.localPending {
		r.entry.pending--
		r.localPending = false
	}
	if w.m[r.key] != r.entry {
		return
	}
	if reset {
		w.removeLocal(r.key, r.entry)
	} else if r.localIncrement && r.entry.n > 0 {
		r.entry.n--
	}
}

// 共享结果返回后才确认影子增量，避免乱序完成把其他已准入请求的影子退掉。
// 在途计数已先保留；断网回退必须扣掉自己后仍有本地位置。
func (r *Reservation) confirmLocal(shared bool) bool {
	w := r.window
	w.mu.Lock()
	defer w.mu.Unlock()
	if r.localPending {
		r.entry.pending--
		r.localPending = false
	}
	if w.m[r.key] != r.entry || w.now().Sub(r.entry.start) >= w.window {
		return shared
	}
	if !shared && r.entry.n+r.entry.pending >= w.limit {
		return false
	}
	if r.entry.n < w.limit {
		r.entry.n++
		r.localIncrement = true
	}
	return true
}

// Undo 退回当次机会；过期、重置后的旧凭据以及重复调用不影响新窗口。
func (r *Reservation) Undo() { r.finish(false) }

// Reset 清掉当次窗口；共享失败时仍清本地影子，不把操作改投到新的后端。
func (r *Reservation) Reset() { r.finish(true) }

func (r *Reservation) finish(reset bool) {
	if r == nil {
		return
	}
	r.once.Do(func() {
		r.releaseLocal(reset)
		if !r.shared {
			return
		}
		w := r.window
		mode := "undo"
		if reset {
			mode = "reset"
		}
		_ = w.shared.client.Do(context.Background(), func(ctx context.Context, rdb redis.Cmdable) error {
			return rdb.Eval(ctx, finishWindowScript, w.shared.keys,
				r.field, w.shared.namespace, w.now().UnixMilli(), r.generation, mode).Err()
		})
	})
}

const windowScriptHelpers = `
local function decode(raw)
  local expiry, gen, count = string.match(raw, '^([^|]+)|([^|]+)|([^|]+)$')
  return tonumber(expiry), gen, tonumber(count)
end
local function encode(expiry, gen, count)
  return string.format('%.0f|%s|%.0f', expiry, gen, count)
end
local function remove(field)
  if redis.call('HDEL', KEYS[1], field) == 1 then
    local ns = string.sub(field, 1, 64)
    if redis.call('HINCRBY', KEYS[3], ns, -1) <= 0 then
      redis.call('HDEL', KEYS[3], ns)
    end
  end
  redis.call('ZREM', KEYS[2], field)
end
`

// Redis 6 的 Lua 报错不会回滚已写入的数据。固定探测字段先验证全部命令，
// 同一次 EVAL 才继续修改业务记录，避免运行期撤权造成容量与记录半写。
const windowProbeNamespace = "4dc7511cd821ab100e25703e18b23f029edaaed1b8555b624fcc70ef914e8855"
const windowProbeField = windowProbeNamespace + ":ba9c736f19e7f60b7f6764adb0b7908c0a2b394e09b6c09863528c7f2bc86095"

const windowPermissionScript = `
do
  local field = '` + windowProbeField + `'
  local ns = '` + windowProbeNamespace + `'
  redis.call('HSET', KEYS[1], field, '-1|probe|1')
  redis.call('HGET', KEYS[1], field)
  redis.call('HSET', KEYS[3], ns, 0)
  redis.call('HINCRBY', KEYS[3], ns, 1)
  redis.call('ZADD', KEYS[2], -1, field)
  redis.call('ZRANGEBYSCORE', KEYS[2], -1, -1, 'LIMIT', 0, 1)
  redis.call('ZREM', KEYS[2], field)
  redis.call('HDEL', KEYS[1], field)
  redis.call('HDEL', KEYS[3], ns)
end
`

const reserveWindowScript = windowPermissionScript + windowScriptHelpers + `
local field, ns, now = ARGV[1], ARGV[2], tonumber(ARGV[3])
local span, limit, cap = tonumber(ARGV[4]), tonumber(ARGV[5]), tonumber(ARGV[6])
local expired = redis.call('ZRANGEBYSCORE', KEYS[2], '-inf', now, 'LIMIT', 0, tonumber(ARGV[8]))
for _, old in ipairs(expired) do remove(old) end
local raw = redis.call('HGET', KEYS[1], field)
if raw then
  local expiry, gen, count = decode(raw)
  if expiry <= now then
    remove(field)
  else
    if count >= limit then return {0, ''} end
    redis.call('HSET', KEYS[1], field, encode(expiry, gen, count + 1))
    return {1, gen}
  end
end
if (tonumber(redis.call('HGET', KEYS[3], ns)) or 0) >= cap then return {0, ''} end
local expiry = now + span
redis.call('HSET', KEYS[1], field, encode(expiry, ARGV[7], 1))
redis.call('ZADD', KEYS[2], expiry, field)
redis.call('HINCRBY', KEYS[3], ns, 1)
return {1, ARGV[7]}
`

const finishWindowScript = windowPermissionScript + windowScriptHelpers + `
local raw = redis.call('HGET', KEYS[1], ARGV[1])
if not raw then return 0 end
local expiry, gen, count = decode(raw)
if expiry <= tonumber(ARGV[3]) or gen ~= ARGV[4] then return 0 end
if ARGV[5] == 'reset' then
  remove(ARGV[1])
elseif count > 0 then
  redis.call('HSET', KEYS[1], ARGV[1], encode(expiry, gen, count - 1))
end
return 1
`

// Probe 在实际固定键上验证 EVAL 与窗口脚本的全部命令，再仅清掉探测字段。
// 探测字段固定，权限故障持续时也不会不断创建新的字段。
func Probe(ctx context.Context, rdb redis.Cmdable, key func(...string) string) error {
	keys := windowKeys(key)
	defer func() {
		_ = rdb.HDel(ctx, keys[0], windowProbeField).Err()
		_ = rdb.HDel(ctx, keys[2], windowProbeNamespace).Err()
		_ = rdb.ZRem(ctx, keys[1], windowProbeField).Err()
	}()
	return rdb.Eval(ctx, probeWindowScript, keys).Err()
}

const probeWindowScript = windowPermissionScript + `return 1`
