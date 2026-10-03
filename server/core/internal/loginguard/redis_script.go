package loginguard

// 脚本只用 Redis 6 的命令；逻辑记录与票据共用固定容器，过期索引每次最多处理 128 项。
// 时间由应用传入，多个实例的失败时间按值保留；票据释放和结果写入只更新已有记录。
const guardScript = `
local data, expiry, capacity = KEYS[1], KEYS[2], KEYS[3]
local op, ns = ARGV[1], ARGV[2]
local function probe_permissions()
    local probe = '_probe'
    -- 脚本错误不会回滚。先确认清理权限，固定字段确保连续失败也不会增加记录。
    redis.call('HDEL', data, probe)
    redis.call('HDEL', capacity, probe)
    redis.call('ZREM', expiry, probe)
    redis.call('HGET', data, probe)
    -- 即使后面的写权限失败，留下的也只是合法的过期票据，正常 GC 可以安全删除。
    redis.call('HSET', data, probe, cjson.encode({k='t', n=probe, pair=probe..':p', acct=probe..':a', e=0}))
    redis.call('HSET', capacity, probe, 0)
    redis.call('HINCRBY', capacity, probe, 1)
    redis.call('ZADD', expiry, 0, probe)
    redis.call('ZRANGEBYSCORE', expiry, 0, 0, 'LIMIT', 0, 1)
    redis.call('ZREM', expiry, probe)
    redis.call('HDEL', data, probe)
    redis.call('HDEL', capacity, probe)
end
-- ACL 变更不会插入同一个脚本中；业务写入前完整验证，避免撤权后半写用户计数。
probe_permissions()
if op == 'probe' then
    return {1, 0, 0, 0}
end
local now, budget = tonumber(ARGV[3]), tonumber(ARGV[4])
local window, duration = tonumber(ARGV[9]), tonumber(ARGV[10])
local iplimit, acctlimit = tonumber(ARGV[11]), tonumber(ARGV[12])
local captcha, pairlimit, failacctlimit = tonumber(ARGV[13]), tonumber(ARGV[14]), tonumber(ARGV[15])
local always, lease = ARGV[16] == '1', tonumber(ARGV[17])
local ip = ns .. ':r:i:' .. ARGV[5]
local rateacct = ns .. ':r:a:' .. ARGV[6]
local acct = ns .. ':s:a:' .. ARGV[6]
local pair = ns .. ':s:p:' .. ARGV[7]
local ticket = ns .. ':t:' .. ARGV[8]

local function read(field)
    local value = redis.call('HGET', data, field)
    if value then return cjson.decode(value) end
    return nil
end
local function erase(field, r)
    redis.call('HDEL', data, field)
    redis.call('ZREM', expiry, field)
    if r.k ~= 't' then
        local count = redis.call('HINCRBY', capacity, r.n, -1)
        if count == 0 then redis.call('HDEL', capacity, r.n) end
    end
end
local function trim(r, current)
    local keep = {}
    for _, stamp in ipairs(r.f) do
        if stamp >= current - r.w then table.insert(keep, stamp) end
    end
    r.f = keep
    if r.u < current then r.u = 0 end
    -- 最大租期已过时所有旧 pin 都无效，推进代数，使迟到票据不能释放新的 pin。
    if r.p > 0 and r.e <= current then
        r.p, r.v = 0, r.v + 1
    end
end
local function write_state(field, r, current)
    trim(r, current)
    if r.p == 0 and #r.f == 0 and r.u == 0 then
        erase(field, r)
        return
    end
    local deadline = r.u + 1
    for _, stamp in ipairs(r.f) do deadline = math.max(deadline, stamp + r.w + 1) end
    if r.p > 0 then deadline = math.max(deadline, r.e) end
    redis.call('HSET', data, field, cjson.encode(r))
    redis.call('ZADD', expiry, deadline, field)
end
local function release(t, current)
    for i, field in ipairs({t.pair, t.acct}) do
        local r = read(field)
        if r and r.k == 's' then
            trim(r, current)
            local generation = i == 1 and t.pg or t.ag
            local version = i == 1 and t.pv or t.av
            if r.g == generation and r.v == version and r.p > 0 then r.p = r.p - 1 end
            write_state(field, r, current)
        end
    end
end
local expired = redis.call('ZRANGEBYSCORE', expiry, '-inf', now, 'LIMIT', 0, 128)
for _, field in ipairs(expired) do
    local r = read(field)
    if not r then
        redis.call('ZREM', expiry, field)
    elseif r.k == 't' then
        if r.e <= now then
            erase(field, r)
            release(r, now)
        end
    elseif r.k == 'r' then
        if r.s + 60000 <= now then erase(field, r) end
    else
        write_state(field, r, now)
    end
end

local function state(field)
    local r = read(field)
    if r then trim(r, now) end
    return r
end
local function allow(field, limit)
    local r = read(field)
    if not r then
        r = {k='r', n=ns, s=now, c=0}
        redis.call('HINCRBY', capacity, ns, 1)
    elseif now - r.s >= 60000 then
        r.s, r.c = now, 0
    end
    r.c = r.c + 1
    redis.call('HSET', data, field, cjson.encode(r))
    redis.call('ZADD', expiry, r.s + 60000, field)
    return r.c <= limit
end
local function pin(field, r)
    if not r then
        r = {k='s', n=ns, f={}, u=0, p=0, w=window, e=0, g=ARGV[8], v=0}
        redis.call('HINCRBY', capacity, ns, 1)
    end
    r.p = r.p + 1
    r.e = math.max(r.e, now + lease)
    write_state(field, r, now)
    return r
end

if op == 'check' or op == 'admit' then
    local pr, ar = state(pair), state(acct)
    local over, ratestart = false, 0
    if op == 'check' then
        if pr and pr.u ~= 0 then return {2, pr.u, 0, 1} end
        if ar and ar.u ~= 0 then return {2, ar.u, 0, 1} end
    end
    local need = 0
    local fields = {pair, acct}
    if op == 'check' then table.insert(fields, ip); table.insert(fields, rateacct) end
    for _, field in ipairs(fields) do
        if not redis.call('HGET', data, field) then need = need + 1 end
    end
    local count = tonumber(redis.call('HGET', capacity, ns) or '0')
    if count + need > budget then return {4, 0, 0, 0} end
    if op == 'check' then
        if not allow(ip, iplimit) then return {3, 0, 0, 0} end
        -- 账号的次数超限不拒绝，改为这次必须带验证码；票据记下窗口起点，验证码没过时凭它退回这一次。
        over = not allow(rateacct, acctlimit)
        ratestart = read(rateacct).s
    end
    pr, ar = pin(pair, pr), pin(acct, ar)
    local t = {k='t', n=ns, pair=pair, acct=acct, e=now+lease, pg=pr.g, pv=pr.v, ag=ar.g, av=ar.v, rs=ratestart}
    redis.call('HSET', data, ticket, cjson.encode(t))
    redis.call('ZADD', expiry, t.e, ticket)
    local requires = always or over or #pr.f >= captcha
    return {1, 0, requires and 1 or 0, 0}
end

local t = read(ticket)
if not t or t.e <= now or t.pair ~= pair or t.acct ~= acct then return {0, 0, 0, 0} end
if op == 'refund' then
    -- 只退这张票据记进去的那一次：窗口已经换了就不动；只改已有的窗口，不新建。
    local w = read(rateacct)
    if w and w.k == 'r' and t.rs and t.rs ~= 0 and w.s == t.rs and w.c > 0 then
        w.c = w.c - 1
        redis.call('HSET', data, rateacct, cjson.encode(w))
    end
end
local pr, ar = state(pair), state(acct)
-- 准入时已建好失败记录；任何迟到结果都不能重建它们。
erase(ticket, t)
if not pr or not ar or pr.p <= 0 or ar.p <= 0 or pr.g ~= t.pg or pr.v ~= t.pv or ar.g ~= t.ag or ar.v ~= t.av then
    release(t, now)
    return {0, 0, 0, 0}
end
local requires, locked = false, false
if op == 'fail' then
    local function fail(r, limit, keep)
        table.insert(r.f, now)
        table.sort(r.f)
        while #r.f > keep do table.remove(r.f, 1) end
        if #r.f >= limit then
            r.u = math.max(r.u, now + duration)
            return true
        end
        return false
    end
    local pl = fail(pr, pairlimit, math.max(pairlimit, captcha))
    local al = fail(ar, failacctlimit, failacctlimit)
    locked = pl or al
    requires = always or #pr.f >= captcha
elseif op == 'succeed' then
    pr.f, pr.u = {}, 0
end
pr.p, ar.p = pr.p - 1, ar.p - 1
write_state(pair, pr, now)
write_state(acct, ar, now)
return {1, 0, requires and 1 or 0, locked and 1 or 0}
`
