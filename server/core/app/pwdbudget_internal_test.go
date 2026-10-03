package app

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/internal/authimpl"
	"github.com/goalladmin/goalladmin/server/core/portal"
)

// 规范 §13.2 第 150 条（D-068）：一个进程只有一组密码计算的位置。登录核对密码、后台生成密码哈希
// （auth.Service.HashPassword）、开主体和重置主账号密码的初始密码（org.NewInitialPassword）占的是同一组：
// 占满时三条路都回 429，让出之后照常；后台算完哈希会把位置让出来。
func TestAuth_150_HashingSharesPasswordBudget(t *testing.T) {
	f := newAuthFixture(t, func(a *App) { a.pwdParallel = 1; a.pwdWait = 30 * time.Millisecond })
	f.addUser("bob", "correct-horse-9")
	deps := f.app.deps

	leave, ok := f.app.pwdBudget.TryEnterFront()
	require.True(t, ok, "占住唯一的位置：相当于有一个密码计算正在进行")
	_, err := deps.Auth.HashPassword(testPortal, "Some-pass-12")
	require.ErrorIs(t, err, httpx.ErrTooManyRequests, "后台生成哈希")
	_, err = deps.Orgs.NewInitialPassword()
	require.ErrorIs(t, err, httpx.ErrTooManyRequests, "主体的初始密码")
	busy := f.login("bob", "correct-horse-9")
	require.Equal(t, 429, busy.rec.Code, "登录用的是同一组位置: %s", busy.rec.Body.String())
	require.Equal(t, httpx.CodeTooManyRequests, busy.env.Code)
	leave()

	// 让出之后三条路都照常；每一条算完都把位置让出来（只有一个位置，漏了一次后面的就全是 429）
	for range 3 {
		hash, err := deps.Auth.HashPassword(testPortal, "Some-pass-12")
		require.NoError(t, err)
		require.NotEmpty(t, hash)
		pwd, err := deps.Orgs.NewInitialPassword()
		require.NoError(t, err)
		require.NotEmpty(t, pwd.Plain())
		access, _ := f.mustLogin("bob", "correct-horse-9")
		require.NotEmpty(t, access)
	}
	leave, ok = f.app.pwdBudget.TryEnterFront()
	require.True(t, ok, "位置都让出来了")
	leave()
}

// 规范 §13.2 第 156 条（D-070）：位置的总数取配置 server.passwordParallel。
func TestAuth_156_PasswordParallelFromConfig(t *testing.T) {
	f := newAuthFixtureCfg(t, func(cfg *conf.Config) { cfg.Server.PasswordParallel = 3 })
	leaves := make([]func(), 0, 3)
	for i := range 3 {
		leave, ok := f.app.pwdBudget.TryEnterFront()
		require.True(t, ok, "第 %d 个", i+1)
		leaves = append(leaves, leave)
	}
	_, ok := f.app.pwdBudget.TryEnterFront()
	require.False(t, ok, "配置的是 3 个")
	for _, l := range leaves {
		l()
	}
}

// 规范 §13.2 第 157 条（D-071）：登录、解锁可以用全部位置，满了等一小会儿，等到了照常做、等不到才回 429；
// 本人改密、后台生成哈希最多占一半，满了直接回 429、不等，什么都不变。被挡的不占次数。
func TestAuth_157_LoginWaitsOthersDoNot(t *testing.T) {
	const wait = 800 * time.Millisecond
	f := newAuthFixture(t, func(a *App) { a.pwdParallel = 4; a.pwdWait = wait })
	id := f.addUser("bob", "correct-horse-9")
	access, _ := f.mustLogin("bob", "correct-horse-9")
	b, deps := f.app.pwdBudget, f.app.deps
	hashOf := func() string {
		f.users.mu.Lock()
		defer f.users.mu.Unlock()
		return f.users.byID[id].PasswordHash
	}
	timed := func(fn func()) time.Duration {
		start := time.Now()
		fn()
		return time.Since(start)
	}
	changePwd := func(old, pwd string) resp {
		return f.do("PUT", "/auth/password", gin.H{"oldPassword": old, "newPassword": pwd}, bearerOpt(access))
	}

	// 后一类占了一半（2 个）：再来的后一类直接 429、不等；登录不受影响
	bg1, ok := b.EnterBackground()
	require.True(t, ok)
	bg2, ok := b.EnterBackground()
	require.True(t, ok)
	before := hashOf()
	require.Less(t, timed(func() {
		_, err := deps.Auth.HashPassword(testPortal, "Some-pass-12")
		require.ErrorIs(t, err, httpx.ErrTooManyRequests)
		_, err = deps.Orgs.NewInitialPassword()
		require.ErrorIs(t, err, httpx.ErrTooManyRequests)
		r := changePwd("correct-horse-9", "Another-horse-10")
		require.Equal(t, 429, r.rec.Code, r.rec.Body.String())
	}), wait/2, "后一类满了不等")
	require.Equal(t, before, hashOf(), "被挡的改密什么都没换")
	access2, _ := f.mustLogin("bob", "correct-horse-9")
	require.NotEmpty(t, access2, "另一半位置留给登录")

	// 全部占满：登录等位置，等到了照常登录
	fr1, ok := b.TryEnterFront()
	require.True(t, ok)
	fr2, ok := b.TryEnterFront()
	require.True(t, ok)
	done := make(chan resp, 1)
	go func() { done <- f.login("bob", "correct-horse-9") }()
	select {
	case r := <-done:
		t.Fatalf("位置全满时登录没有等就返回了: %s", r.rec.Body.String())
	case <-time.After(wait / 4):
	}
	fr1()
	r := <-done
	require.Equal(t, 0, r.env.Code, "等到了让出来的位置: %s", r.rec.Body.String())

	// 一直没有位置：等够了回 429
	fr1, ok = b.TryEnterFront()
	require.True(t, ok, "刚才那次登录算完把位置让出来了")
	d := timed(func() {
		r = f.login("bob", "correct-horse-9")
		require.Equal(t, 429, r.rec.Code, r.rec.Body.String())
		require.Equal(t, httpx.CodeTooManyRequests, r.env.Code)
	})
	require.GreaterOrEqual(t, d, wait*9/10, "等够了时限才拒绝")

	// 解锁和登录同等对待：也等，等不到 429，不占解锁次数
	require.Equal(t, 0, f.do("POST", "/auth/lock", nil, bearerOpt(access)).env.Code)
	unlock := func(pwd string) resp {
		return f.do("POST", "/auth/unlock", gin.H{"password": pwd}, bearerOpt(access))
	}
	d = timed(func() { require.Equal(t, 429, unlock("correct-horse-9").rec.Code) })
	require.GreaterOrEqual(t, d, wait*9/10)

	for _, leave := range []func(){bg1, bg2, fr1, fr2} {
		leave()
	}
	// 位置让出来之后：解锁的 5 次机会一次没少（被挡的那次没占），输错 4 次还能解开
	for i := range authimpl.MaxUnlockFailures - 1 {
		require.Equal(t, httpx.CodeValidation, unlock("wrong-password-9").env.Code, "第 %d 次", i+1)
	}
	require.Equal(t, 0, unlock("correct-horse-9").env.Code)
	// 改密的 5 次机会也没少，这次照常改成
	for i := range pwdTriesLimitForTest - 1 {
		require.Equal(t, httpx.CodeValidation, changePwd("wrong-password-9", "Another-horse-10").env.Code, "第 %d 次", i+1)
	}
	require.Equal(t, 0, changePwd("correct-horse-9", "Another-horse-10").env.Code)
	require.NotEqual(t, before, hashOf())
}

// 规范 §13.2 第 157 条（D-071）：登录核对不过时，位置在记失败、写登录日志之前就让出来了——登录日志表被别人锁着、
// 这次请求卡在写日志上的时候，唯一的那个位置是空的。
func TestAuth_157_SlotReleasedBeforeFailureIsLogged(t *testing.T) {
	f := newAuthFixture(t, func(a *App) { a.pwdParallel = 1; a.pwdWait = 30 * time.Millisecond })
	f.addUser("bob", "correct-horse-9")
	sqlDB, err := f.app.deps.DB.DB()
	require.NoError(t, err)
	ctx := context.Background()
	conn, err := sqlDB.Conn(ctx)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	_, err = conn.ExecContext(ctx, "LOCK TABLES ga_login_log WRITE")
	require.NoError(t, err)
	unlocked := false
	unlock := func() {
		if !unlocked {
			unlocked = true
			_, _ = conn.ExecContext(ctx, "UNLOCK TABLES")
		}
	}
	defer unlock()

	done := make(chan resp, 1)
	go func() { done <- f.login("bob", "wrong-password-9") }()
	// 等到这次请求真的卡在写登录日志上（在等表锁）
	require.Eventually(t, func() bool {
		var n int64
		err := f.app.deps.DB.Raw("SELECT COUNT(*) FROM information_schema.processlist WHERE state LIKE 'Waiting for table%' AND info LIKE '%ga_login_log%'").Scan(&n).Error
		return err == nil && n > 0
	}, 2*time.Second, 5*time.Millisecond, "登录请求没有走到写登录日志的地方")
	all, _ := f.app.pwdBudget.InUse()
	require.Zero(t, all, "请求卡在写登录日志上的时候，位置应该已经让出来")
	select {
	case r := <-done:
		t.Fatalf("日志表还锁着，请求就返回了: %s", r.rec.Body.String())
	default:
	}
	unlock()
	r := <-done
	require.Equal(t, httpx.CodeLoginFailed, r.env.Code, r.rec.Body.String())
}

// pwdTriesLimitForTest 是同一会话 15 分钟内核对旧密码的次数上限（D-055，authimpl 里的常量没有导出）。
const pwdTriesLimitForTest = 5

// 规范 §13.2 第 157 条（D-071）：位置只在计算的时候占着——登录、解锁、本人改密走到事务里等行锁时，位置已经让出来了。
func TestOrgAuth_157_SlotReleasedBeforeTransaction(t *testing.T) {
	f := newOrgFixture(t, func(a *App) { a.pwdParallel = 1; a.pwdWait = 30 * time.Millisecond })
	_, _, _, staffA, _ := f.seedTwoOrgs()
	access, _ := f.mustShopLogin("M10000001", "staff", "staff-pass-1")

	check := func(name string, call func() resp) {
		t.Helper()
		blocked, release := make(chan struct{}, 1), make(chan struct{})
		f.orgs.setOnLock(func(id uint64) {
			if id == staffA {
				blocked <- struct{}{}
				<-release
			}
		})
		done := make(chan resp, 1)
		go func() { done <- call() }()
		select {
		case <-blocked:
		case <-time.After(5 * time.Second):
			f.orgs.setOnLock(nil)
			close(release)
			t.Fatalf("%s：请求没有走到事务里锁账号行的地方", name)
		}
		leave, ok := f.app.pwdBudget.TryEnterFront()
		f.orgs.setOnLock(nil)
		close(release)
		r := <-done
		require.True(t, ok, "%s：停在事务里等行锁时，唯一的位置应该已经让出来", name)
		leave()
		require.Equal(t, 0, r.env.Code, "%s: %s", name, r.rec.Body.String())
	}
	check("登录", func() resp { return f.shopLogin("M10000001", "staff", "staff-pass-1") })
	require.Equal(t, 0, f.shop("POST", "/auth/lock", nil, bearerOpt(access)).env.Code)
	check("解锁", func() resp {
		return f.shop("POST", "/auth/unlock", gin.H{"password": "staff-pass-1"}, bearerOpt(access))
	})
	check("改密", func() resp {
		return f.shop("PUT", "/auth/password", gin.H{"oldPassword": "staff-pass-1", "newPassword": "staff-pass-2"}, bearerOpt(access))
	})
}

// 规范 §13.2 第 158 条（D-071）：同一个账号 15 分钟内最多成功改密 5 次，之后回 429、不做计算、记安全事件；
// 没改成的不占次数；换一个会话也绕不过去；别的账号不受影响；被要求改密的那一次不挡也不计；过了 15 分钟重新计。
func TestAuth_158_SuccessfulPasswordChangesAreLimited(t *testing.T) {
	f := newAuthFixture(t)
	id := f.addUser("alice", "alice-pass-000")
	f.addUser("bob", "bob-secret-pass-1")
	hashOf := func() string {
		f.users.mu.Lock()
		defer f.users.mu.Unlock()
		return f.users.byID[id].PasswordHash
	}
	change := func(tok, old, pwd string) resp {
		return f.do("PUT", "/auth/password", gin.H{"oldPassword": old, "newPassword": pwd}, bearerOpt(tok))
	}
	access, _ := f.mustLogin("alice", "alice-pass-000")
	cur := "alice-pass-000"
	for i := 1; i <= 5; i++ {
		// 没改成的不占次数：每次成功之前先输错一次旧密码、再给一次不合规的新密码
		require.Equal(t, httpx.CodeValidation, change(access, "wrong-old-pass-1", "alice-pass-999").env.Code)
		require.Equal(t, httpx.CodeValidation, change(access, cur, "short").env.Code)
		next := fmt.Sprintf("alice-pass-%03d", i)
		require.Equal(t, 0, change(access, cur, next).env.Code, "第 %d 次改密", i)
		cur = next
	}
	before := hashOf()
	r := change(access, cur, "alice-pass-006")
	require.Equal(t, 429, r.rec.Code, "第 6 次: %s", r.rec.Body.String())
	require.Equal(t, httpx.CodeTooManyRequests, r.env.Code)
	require.Equal(t, before, hashOf(), "密码没换")
	require.Contains(t, f.securityEvents(), "pwd_change_throttled")

	// 换一个会话也一样（按账号计）；别的账号不受影响
	access2, _ := f.mustLogin("alice", cur)
	require.Equal(t, 429, change(access2, cur, "alice-pass-006").rec.Code)
	bob, _ := f.mustLogin("bob", "bob-secret-pass-1")
	require.Equal(t, 0, change(bob, "bob-secret-pass-1", "bob-secret-pass-2").env.Code)

	// 被次数挡住的请求不占"核对旧密码"的机会（连旧密码都没核对）：挡上 6 次，后面该放行的照样放行
	for range pwdTriesLimitForTest + 1 {
		require.Equal(t, 429, change(access2, cur, "alice-pass-006").rec.Code)
	}
	// 被要求改密的账号（管理员刚重置的）不挡也不计：那一次是系统要他改的
	f.users.set(id, func(a *portal.Account) { a.MustChangePwd = true })
	require.Equal(t, 0, change(access2, cur, "alice-pass-900").env.Code, "必须改密的那一次照常能改")
	cur = "alice-pass-900"
	require.Equal(t, 429, change(access2, cur, "alice-pass-007").rec.Code, "改完之后又回到限制里")
	require.Equal(t, httpx.CodeTooManyRequests, change(access2, "wrong-old-pass-1", "alice-pass-007").env.Code, "被挡的时候连旧密码都不核对")

	// 过了 15 分钟重新计
	f.clock.Advance(15*time.Minute + time.Second)
	access3, _ := f.mustLogin("alice", cur)
	require.Equal(t, 0, change(access3, cur, "alice-pass-006").env.Code)
}

// 规范 §13.2 第 159 条（D-071）：主体端按主体限并发——本人改密、解锁每个主体同时各最多 2 个，满了直接回 429、
// 什么都不变、不占次数；别的主体照常；平台端（不是主体端）不受这两个上限约束。
func TestOrgAuth_159_PerOrgConcurrency(t *testing.T) {
	f := newOrgFixture(t, func(a *App) { a.pwdParallel = 64 }) // 位置够多：挡住请求的只能是主体自己的上限
	orgA, orgB, _, _, _ := f.seedTwoOrgs()
	const n = 3
	pwd := make([]string, n) // 甲的 3 个员工各自当前的密码
	tok := make([]string, n)
	ids := make([]uint64, n)
	for i := range n {
		name := fmt.Sprintf("worker%d", i)
		pwd[i] = "worker-pass-123"
		ids[i] = f.orgs.addUser(orgA, name, f.hash(pwd[i]), false)
		tok[i], _ = f.mustShopLogin("M10000001", name, pwd[i])
	}
	f.orgs.addUser(orgB, "worker", f.hash("worker-pass-123"), false)
	bTok, _ := f.mustShopLogin("M10000002", "worker", "worker-pass-123")
	hashOf := func(id uint64) string {
		f.orgs.mu.Lock()
		defer f.orgs.mu.Unlock()
		return f.orgs.users[id].PasswordHash
	}

	// hold 让请求停在事务里锁账号行的地方：这时请求还占着主体的位置。测试中途失败也会放行，不留停着的请求
	hold := func() (entered chan uint64, release func()) {
		entered = make(chan uint64, 16)
		gate := make(chan struct{})
		f.orgs.setOnLock(func(id uint64) { entered <- id; <-gate })
		var once sync.Once
		release = func() { once.Do(func() { f.orgs.setOnLock(nil); close(gate) }) }
		t.Cleanup(release)
		return entered, release
	}
	waitEntered := func(entered chan uint64, k int, msg string) {
		t.Helper()
		for range k {
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal(msg)
			}
		}
	}
	quick := func(call func() resp) resp {
		t.Helper()
		got := make(chan resp, 1)
		go func() { got <- call() }()
		select {
		case r := <-got:
			return r
		case <-time.After(5 * time.Second):
			t.Fatal("主体的位置满了，请求没有立即返回")
			return resp{}
		}
	}

	// ---- 本人改密：同时 2 个 ----
	changePwd := func(tok, old, to string) resp {
		return f.shop("PUT", "/auth/password", gin.H{"oldPassword": old, "newPassword": to}, bearerOpt(tok))
	}
	entered, release := hold()
	done := make(chan resp, 8)
	for i := range 2 {
		go func() { done <- changePwd(tok[i], pwd[i], "worker-pass-456") }()
	}
	waitEntered(entered, 2, "甲的两个改密没有都进去")
	before := hashOf(ids[2])
	r := quick(func() resp { return changePwd(tok[2], pwd[2], "worker-pass-456") })
	require.Equal(t, 429, r.rec.Code, "甲的第 3 个改密: %s", r.rec.Body.String())
	require.Equal(t, httpx.CodeTooManyRequests, r.env.Code)
	require.Equal(t, before, hashOf(ids[2]), "被挡的改密什么都没换")
	go func() { done <- changePwd(bTok, "worker-pass-123", "worker-pass-456") }()
	waitEntered(entered, 1, "甲占满之后乙的改密没有进去")
	release()
	for range 3 {
		r := <-done
		require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	}
	pwd[0], pwd[1] = "worker-pass-456", "worker-pass-456"
	// 位置让出来了；刚才被挡的那次没有占"核对旧密码"的 5 次机会：再输错 4 次，第 5 次还能改成
	for i := range pwdTriesLimitForTest - 1 {
		require.Equal(t, httpx.CodeValidation, changePwd(tok[2], "wrong-password-9", "worker-pass-456").env.Code, "第 %d 次", i+1)
	}
	require.Equal(t, 0, changePwd(tok[2], pwd[2], "worker-pass-456").env.Code)
	pwd[2] = "worker-pass-456"

	// ---- 平台端没有主体：三个人同时改密都占得到位置，不受"每个主体 2 个"约束 ----
	var platTok [3]string
	for i := range platTok {
		name := fmt.Sprintf("plat%d", i)
		f.addUser(name, "plat-secret-pass-1")
		platTok[i], _ = f.mustLogin(name, "plat-secret-pass-1")
		require.Equal(t, 200, f.ping(platTok[i]).rec.Code) // 账号状态进缓存：下面改密时第一次读账号是在占好位置之后
	}
	platIn, platGo := make(chan struct{}), make(chan struct{})
	var platOnce sync.Once
	platRelease := func() { platOnce.Do(func() { close(platGo) }) }
	t.Cleanup(platRelease)
	f.users.mu.Lock()
	f.users.afterFind = func() { close(platIn); <-platGo } // 第一个停在读账号的地方（拿着这个用户来源的锁），后两个排在它后面
	f.users.mu.Unlock()
	platDone := make(chan resp, 3)
	for i := range platTok {
		go func() {
			platDone <- f.do("PUT", "/auth/password", gin.H{"oldPassword": "plat-secret-pass-1", "newPassword": "plat-secret-pass-2"}, bearerOpt(platTok[i]))
		}()
	}
	<-platIn
	require.Eventually(t, func() bool {
		_, back := f.app.pwdBudget.InUse()
		return back == 3
	}, 10*time.Second, 5*time.Millisecond, "三个平台端的改密应该同时占着位置（没有被主体的上限挡在外面）")
	platRelease()
	for range 3 {
		r := <-platDone
		require.Equal(t, 0, r.env.Code, "平台端的改密不按主体限: %s", r.rec.Body.String())
	}

	// ---- 解锁：同时 2 个 ----
	for i := range n {
		require.Equal(t, 0, f.shop("POST", "/auth/lock", nil, bearerOpt(tok[i])).env.Code)
	}
	require.Equal(t, 0, f.shop("POST", "/auth/lock", nil, bearerOpt(bTok)).env.Code)
	unlock := func(tok, pwd string) resp {
		return f.shop("POST", "/auth/unlock", gin.H{"password": pwd}, bearerOpt(tok))
	}
	entered, release = hold()
	for i := range 2 {
		go func() { done <- unlock(tok[i], pwd[i]) }()
	}
	waitEntered(entered, 2, "甲的两个解锁没有都进去")
	r = quick(func() resp { return unlock(tok[2], pwd[2]) })
	require.Equal(t, 429, r.rec.Code, "甲的第 3 个解锁: %s", r.rec.Body.String())
	go func() { done <- unlock(bTok, "worker-pass-456") }()
	waitEntered(entered, 1, "甲占满之后乙的解锁没有进去")
	release()
	for range 3 {
		r := <-done
		require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	}
	// 被挡的那次没占解锁次数：输错 4 次之后还能解开
	for i := range authimpl.MaxUnlockFailures - 1 {
		require.Equal(t, httpx.CodeValidation, unlock(tok[2], "wrong-password-9").env.Code, "第 %d 次", i+1)
	}
	require.Equal(t, 0, unlock(tok[2], pwd[2]).env.Code)
}
