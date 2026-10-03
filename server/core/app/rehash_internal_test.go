package app

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/internal/password"
	"github.com/goalladmin/goalladmin/server/core/logx"
	"github.com/goalladmin/goalladmin/server/core/portal"
	"github.com/goalladmin/goalladmin/server/core/rbac"
	"github.com/goalladmin/goalladmin/server/migrations"
)

// legacyHash 生成一份升级前的那种 bcrypt 哈希。
func legacyHash(t *testing.T, plain string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.MinCost)
	require.NoError(t, err)
	return string(h)
}

// 规范 §13.2 第 155 条（D-070）：升级前留下的 bcrypt 哈希照常登录，登录成功时换成 Argon2id——只换哈希，"必须改密"不动，
// 会话照常；密码错的不换；已经是当前参数的不重写；参数不是当前的 Argon2id 哈希也升级。
func TestAuth_155_LegacyHashUpgradedOnLogin(t *testing.T) {
	f := newAuthFixture(t)
	hashOf := func(id uint64) string {
		f.users.mu.Lock()
		defer f.users.mu.Unlock()
		return f.users.byID[id].PasswordHash
	}
	old := legacyHash(t, "correct-horse-9")
	id := f.users.add("alice", old)
	f.users.set(id, func(a *portal.Account) { a.MustChangePwd = true })

	// 密码错：不升级
	r := f.login("alice", "wrong-password-9")
	require.Equal(t, httpx.CodeLoginFailed, r.env.Code)
	require.Equal(t, old, hashOf(id))
	require.Zero(t, f.users.rehashes)

	// 密码对：登录成功，哈希换成当前参数的 Argon2id，核对得上；必须改密原样
	r = f.login("alice", "correct-horse-9")
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.Equal(t, true, r.data()["mustChangePwd"])
	upgraded := hashOf(id)
	require.True(t, strings.HasPrefix(upgraded, "$argon2id$v=19$m=64,t=1,p=1$"), upgraded)
	require.True(t, f.app.hasher.Verify(upgraded, "correct-horse-9"))
	require.False(t, f.app.hasher.NeedsRehash(upgraded))
	f.users.mu.Lock()
	require.True(t, f.users.byID[id].MustChangePwd, "只换哈希，必须改密不动")
	f.users.mu.Unlock()
	require.Equal(t, 1, f.users.rehashes)
	first, _ := r.data()["accessToken"].(string)

	// 再登录：已经是当前参数的，不重写；前一个会话还在
	access, _ := f.mustLogin("alice", "correct-horse-9")
	require.NotEmpty(t, access)
	require.Equal(t, upgraded, hashOf(id))
	require.Equal(t, 1, f.users.rehashes)
	require.Equal(t, 403, f.ping(first).rec.Code, "第一个会话还有效（必须改密，所以是 403 而不是 401）")

	// 参数不是当前的 Argon2id 哈希（以后调了参数）：同样在登录时升级
	stale, err := password.NewHasher(password.Params{Memory: 128, Time: 2}).Hash("bob-secret-pass-1")
	require.NoError(t, err)
	bob := f.users.add("bob", stale)
	_, _ = f.mustLogin("bob", "bob-secret-pass-1")
	require.NotEqual(t, stale, hashOf(bob))
	require.True(t, strings.HasPrefix(hashOf(bob), "$argon2id$v=19$m=64,t=1,p=1$"))
	require.True(t, f.app.hasher.Verify(hashOf(bob), "bob-secret-pass-1"))
}

// 规范 §13.2 第 155 条（D-070）：核对通过之后、换哈希之前密码被别人换掉了（改密、重置）：升级的那份不能盖掉新密码。
func TestAuth_155_RehashNeverOverwritesNewerPassword(t *testing.T) {
	f := newAuthFixture(t)
	id := f.users.add("alice", legacyHash(t, "correct-horse-9"))
	reset := f.hashOf("reset-by-admin-1")
	// 登录事务里读账号（这个用户来源没有行锁，读到的是旧副本）之后，重置提交了
	f.users.mu.Lock()
	f.users.afterFind = func() { f.users.byID[id].PasswordHash = reset }
	f.users.mu.Unlock()
	r := f.login("alice", "correct-horse-9")
	require.Equal(t, 0, r.env.Code, "读到的还是旧哈希，这次登录照常: %s", r.rec.Body.String())
	f.users.mu.Lock()
	got := f.users.byID[id].PasswordHash
	f.users.mu.Unlock()
	require.Equal(t, reset, got, "升级的那份没有盖掉刚重置的密码")
	require.Equal(t, 1, f.users.rehashes, "调用了，但哈希对不上，什么都没写")
	require.Equal(t, httpx.CodeLoginFailed, f.login("alice", "correct-horse-9").env.Code, "旧密码已经不能用了")
	_, _ = f.mustLogin("alice", "reset-by-admin-1")
}

// plainUsers 是只实现了必需接口的用户来源：没有 PasswordRehasher。
type plainUsers struct{ m *memUsers }

func (p plainUsers) FindByUsername(ctx context.Context, u string) (*portal.Account, error) {
	return p.m.FindByUsername(ctx, u)
}
func (p plainUsers) FindByID(ctx context.Context, id uint64) (*portal.Account, error) {
	return p.m.FindByID(ctx, id)
}
func (p plainUsers) UpdatePasswordHash(ctx context.Context, id uint64, h string, must bool) error {
	return p.m.UpdatePasswordHash(ctx, id, h, must)
}
func (p plainUsers) TouchLogin(context.Context, uint64, string, time.Time) error { return nil }

type plainUsersModule struct{ users portal.UserProvider }

func (m plainUsersModule) Name() string { return "plainusers" }
func (m plainUsersModule) Init(d *Deps) error {
	return d.Portals.Register(portal.Portal{Code: testPortal, Users: m.users})
}
func (m plainUsersModule) Perms() []rbac.Perm          { return nil }
func (m plainUsersModule) Menus() []rbac.MenuNode      { return nil }
func (m plainUsersModule) Routes(*Router)              {}
func (m plainUsersModule) Start(context.Context) error { return nil }
func (m plainUsersModule) Stop(context.Context) error  { return nil }

// 规范 §13.2 第 155 条（D-070）：用户来源没有实现 PasswordRehasher 时不升级，旧哈希照常登录。
func TestAuth_155_ProviderWithoutRehasherKeepsLegacyHash(t *testing.T) {
	gdb := db.OpenTestDB(t)
	_, err := db.MigrateUp(db.WithDB(context.Background(), gdb), gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	cfg := conf.Default()
	cfg.Log.Level = "error"
	cfg.Server.AllowedOrigins = []string{testOrigin}
	cfg.Portals[testPortal] = conf.Portal{AccessTTL: 15 * time.Minute, RefreshTTL: 168 * time.Hour, JWTSecret: testSecret}
	users := newMemUsers()
	a, err := New(cfg, WithDB(gdb), WithLogger(logx.New("error", "text", io.Discard)), WithPasswordHashParams(64, 1))
	require.NoError(t, err)
	a.Register(plainUsersModule{users: plainUsers{users}})
	require.NoError(t, a.Setup())
	f := &authFixture{t: t, app: a, users: users}

	old := legacyHash(t, "correct-horse-9")
	id := users.add("alice", old)
	for range 2 {
		access, _ := f.mustLogin("alice", "correct-horse-9")
		require.NotEmpty(t, access)
	}
	users.mu.Lock()
	defer users.mu.Unlock()
	require.Equal(t, old, users.byID[id].PasswordHash)
	require.Zero(t, users.rehashes)
}

// 规范 §13.2 第 155 条（D-070）：旧哈希在请求途中被另一个登录升级了——哈希变了、密码没变。同时在途的登录、解锁、
// 改密走到事务里时不能把它当成"密码已经换了"：拿这次输入的密码按新哈希再核对一次，核对得上就照常做完。
// 真的换了密码（重置）的照旧按密码不对处理；不是升级造成的哈希变化不重算、直接按换了处理。
func TestOrgAuth_155_ConcurrentUpgradeIsNotAPasswordChange(t *testing.T) {
	f := newOrgFixture(t)
	orgA, _, _, _, _ := f.seedTwoOrgs()
	const pass = "legacy-pass-123"
	id := f.orgs.addUser(orgA, "old", legacyHash(t, pass), false)
	stored := func() string {
		f.orgs.mu.Lock()
		defer f.orgs.mu.Unlock()
		return f.orgs.users[id].PasswordHash
	}
	set := func(hash string) { f.orgs.setUser(id, func(a *portal.Account) { a.PasswordHash = hash }) }
	// meanwhile：请求走到事务里锁账号行的那一刻，库里的哈希已经被别人换成了 with（只换这一次）
	meanwhile := func(with string) {
		var once sync.Once
		f.orgs.setOnLock(func(uid uint64) {
			if uid == id {
				once.Do(func() { set(with); f.orgs.setOnLock(nil) })
			}
		})
	}
	login := func() resp { return f.shopLogin("M10000001", "old", pass) }

	// ---- 登录 ----
	upgraded := f.hash(pass)
	meanwhile(upgraded)
	r := login()
	require.Equal(t, 0, r.env.Code, "另一个登录刚把哈希升级了：这次登录照常成功: %s", r.rec.Body.String())
	require.Equal(t, upgraded, stored(), "已经升级过的不再覆盖")
	access, _ := r.data()["accessToken"].(string)
	logs := f.loginLogs()
	require.True(t, logs[len(logs)-1].Success, "没有记成一次失败的登录")

	set(legacyHash(t, pass))
	reset := f.hash("reset-by-admin-1")
	meanwhile(reset)
	require.Equal(t, httpx.CodeLoginFailed, login().env.Code, "途中密码真的被重置了：按密码错误处理")
	require.Equal(t, reset, stored(), "重置的密码没有被盖掉")

	// 不是升级造成的变化（原来就是当前参数的哈希）：直接按换了处理，哪怕新哈希碰巧是同一个密码的
	set(f.hash(pass))
	meanwhile(f.hash(pass))
	require.Equal(t, httpx.CodeLoginFailed, login().env.Code)

	set(legacyHash(t, pass))
	meanwhile(legacyHash(t, pass)) // 同一个密码的另一份旧哈希：也不是升级，不重算
	require.Equal(t, httpx.CodeLoginFailed, login().env.Code)

	// ---- 解锁（升级之前登录的会话还在，库里还是旧哈希）----
	set(legacyHash(t, pass))
	unlock := func() resp { return f.shop("POST", "/auth/unlock", gin.H{"password": pass}, bearerOpt(access)) }
	require.Equal(t, 0, f.shop("POST", "/auth/lock", nil, bearerOpt(access)).env.Code)
	meanwhile(f.hash(pass))
	require.Equal(t, 0, unlock().env.Code, "途中被升级：照常解开")
	require.Equal(t, 0, f.shop("POST", "/auth/lock", nil, bearerOpt(access)).env.Code)
	set(legacyHash(t, pass))
	meanwhile(reset)
	r = unlock()
	require.Equal(t, httpx.CodeValidation, r.env.Code, "途中密码真的换了：按输错处理: %s", r.rec.Body.String())
	set(legacyHash(t, pass))
	require.Equal(t, 0, unlock().env.Code)

	// ---- 本人改密 ----
	change := func(to string) resp {
		return f.shop("PUT", "/auth/password", gin.H{"oldPassword": pass, "newPassword": to}, bearerOpt(access))
	}
	set(legacyHash(t, pass))
	meanwhile(reset)
	r = change("brand-new-pass-1")
	require.Equal(t, httpx.CodeValidation, r.env.Code, "途中密码真的换了：按旧密码不对处理: %s", r.rec.Body.String())
	require.Equal(t, reset, stored(), "重置的密码没有被这次改密盖掉")
	set(legacyHash(t, pass))
	meanwhile(f.hash(pass))
	require.Equal(t, 0, change("brand-new-pass-1").env.Code, "途中被升级：照常改成")
	require.True(t, f.app.hasher.Verify(stored(), "brand-new-pass-1"))
}

// 规范 §13.2 第 160 条（D-070）：配置选 bcrypt 时，这个程序写进去的新哈希（后台生成的、主体的初始密码、本人改密）都是
// bcrypt；库里已有的 Argon2id 哈希照常登录，登录成功后换回 bcrypt——两种算法之间切换，密码不失效。默认配置是 Argon2id。
func TestAuth_160_AlgorithmChosenByConfig(t *testing.T) {
	require.Equal(t, password.Argon2id, newAuthFixture(t).app.hasher.Algorithm(), "默认是 Argon2id")

	f := newAuthFixtureCfg(t, func(cfg *conf.Config) { cfg.Server.PasswordHash = conf.PasswordHashBcrypt }, WithBcryptCost(4))
	require.Equal(t, password.Bcrypt, f.app.hasher.Algorithm())
	deps := f.app.deps
	isBcrypt := func(hash string) bool { return strings.HasPrefix(hash, "$2a$04$") }
	hashOf := func(id uint64) string {
		f.users.mu.Lock()
		defer f.users.mu.Unlock()
		return f.users.byID[id].PasswordHash
	}

	hash, err := deps.Auth.HashPassword(testPortal, "Some-pass-12")
	require.NoError(t, err)
	require.True(t, isBcrypt(hash), hash)
	pwd, err := deps.Orgs.NewInitialPassword()
	require.NoError(t, err)
	require.NotEmpty(t, pwd.Plain())

	// 库里是 Argon2id 的哈希（之前选的是它）：照常登录，登录后换回 bcrypt
	argon, err := password.NewHasher(password.Params{Memory: 64, Time: 1}).Hash("correct-horse-9")
	require.NoError(t, err)
	id := f.users.add("alice", argon)
	access, _ := f.mustLogin("alice", "correct-horse-9")
	require.True(t, isBcrypt(hashOf(id)), hashOf(id))
	require.True(t, f.app.hasher.Verify(hashOf(id), "correct-horse-9"))
	require.Equal(t, 1, f.users.rehashes)
	// 再登录：已经是选的算法、当前的 cost，不重写
	swapped := hashOf(id)
	_, _ = f.mustLogin("alice", "correct-horse-9")
	require.Equal(t, swapped, hashOf(id))
	require.Equal(t, 1, f.users.rehashes)
	// 密码错的登录：和默认配置一样失败，不换
	require.Equal(t, httpx.CodeLoginFailed, f.login("alice", "wrong-password-9").env.Code)
	require.Equal(t, httpx.CodeLoginFailed, f.login("nobody", "wrong-password-9").env.Code)

	// 本人改密写进去的也是 bcrypt
	r := f.do("PUT", "/auth/password", gin.H{"oldPassword": "correct-horse-9", "newPassword": "Another-horse-10"}, bearerOpt(access))
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.True(t, isBcrypt(hashOf(id)), hashOf(id))
	_, _ = f.mustLogin("alice", "Another-horse-10")
}

// 规范 §13.2 第 161 条（D-072）：配置选 PBKDF2（HMAC-SHA256 或 HMAC-SHA512）时，这个程序写进去的新哈希是选的那一种、
// 生产用的迭代次数；库里已有的 Argon2id、bcrypt、另一种 PBKDF2 的哈希照常登录，登录成功后换成选的那种；换回默认的
// Argon2id 之后，PBKDF2 的哈希同样照常登录、换回去。几种算法之间怎么切换，密码都不失效。
func TestAuth_161_PBKDF2ChosenByConfig(t *testing.T) {
	prod := map[string]string{
		conf.PasswordHashPBKDF2SHA256: "$pbkdf2-sha256$i=600000,l=32$",
		conf.PasswordHashPBKDF2SHA512: "$pbkdf2-sha512$i=220000,l=32$",
	}
	other := map[string]password.Algorithm{
		conf.PasswordHashPBKDF2SHA256: password.PBKDF2SHA512,
		conf.PasswordHashPBKDF2SHA512: password.PBKDF2SHA256,
	}
	// 配置可以取的值和哈希器认识的算法是同一组：配置放行了哈希器不认识的名字，就会悄悄按 Argon2id 算
	names := make([]string, 0, len(password.Algorithms()))
	for _, a := range password.Algorithms() {
		names = append(names, string(a))
	}
	require.Equal(t, conf.PasswordHashes(), names)

	argon, err := password.NewHasher(password.Params{Memory: 64, Time: 1}).Hash("correct-horse-9")
	require.NoError(t, err)
	for _, name := range []string{conf.PasswordHashPBKDF2SHA256, conf.PasswordHashPBKDF2SHA512} {
		t.Run(name, func(t *testing.T) {
			choose := func(cfg *conf.Config) { cfg.Server.PasswordHash = name }
			// 不调参数：生产用的迭代次数
			hash, err := newAuthFixtureCfg(t, choose).app.deps.Auth.HashPassword(testPortal, "Some-pass-12")
			require.NoError(t, err)
			require.True(t, strings.HasPrefix(hash, prod[name]), hash)

			f := newAuthFixtureCfg(t, choose, WithPBKDF2Iterations(50))
			require.Equal(t, password.Algorithm(name), f.app.hasher.Algorithm())
			mine := func(hash string) bool { return strings.HasPrefix(hash, "$"+name+"$i=50,l=32$") }
			stored := func(id uint64) string {
				f.users.mu.Lock()
				defer f.users.mu.Unlock()
				return f.users.byID[id].PasswordHash
			}
			hash, err = f.app.deps.Auth.HashPassword(testPortal, "Some-pass-12")
			require.NoError(t, err)
			require.True(t, mine(hash), hash)

			// 库里是别的算法的哈希：照常登录，登录后换成选的这一种
			sibling, err := password.NewHasher(password.Params{Algorithm: other[name], Iterations: 50}).Hash("correct-horse-9")
			require.NoError(t, err)
			olds := map[string]string{"argon": argon, "bcrypt": legacyHash(t, "correct-horse-9"), "sibling": sibling}
			ids := map[string]uint64{}
			for user, old := range olds {
				ids[user] = f.users.add(user, old)
			}
			for n, user := range []string{"argon", "bcrypt", "sibling"} {
				require.Equal(t, httpx.CodeLoginFailed, f.login(user, "wrong-password-9").env.Code)
				require.Equal(t, olds[user], stored(ids[user]), "密码错的不换")
				_, _ = f.mustLogin(user, "correct-horse-9")
				require.True(t, mine(stored(ids[user])), "%s: %s", user, stored(ids[user]))
				require.True(t, f.app.hasher.Verify(stored(ids[user]), "correct-horse-9"))
				require.Equal(t, n+1, f.users.rehashes)
				// 再登录：已经是选的算法、当前的次数，不重写
				swapped := stored(ids[user])
				_, _ = f.mustLogin(user, "correct-horse-9")
				require.Equal(t, swapped, stored(ids[user]))
				require.Equal(t, n+1, f.users.rehashes)
			}
			require.Equal(t, httpx.CodeLoginFailed, f.login("nobody", "wrong-password-9").env.Code)

			// 本人改密写进去的也是选的这一种
			access, _ := f.mustLogin("argon", "correct-horse-9")
			r := f.do("PUT", "/auth/password", gin.H{"oldPassword": "correct-horse-9", "newPassword": "Another-horse-10"}, bearerOpt(access))
			require.Equal(t, 0, r.env.Code, r.rec.Body.String())
			require.True(t, mine(stored(ids["argon"])), stored(ids["argon"]))
			_, _ = f.mustLogin("argon", "Another-horse-10")

			// 换回默认的 Argon2id：PBKDF2 的哈希照常登录，登录后换回 Argon2id
			back := newAuthFixture(t)
			pb, err := f.app.hasher.Hash("correct-horse-9")
			require.NoError(t, err)
			require.True(t, mine(pb))
			id := back.users.add("carol", pb)
			_, _ = back.mustLogin("carol", "correct-horse-9")
			back.users.mu.Lock()
			now := back.users.byID[id].PasswordHash
			back.users.mu.Unlock()
			require.True(t, strings.HasPrefix(now, "$argon2id$"), now)
			require.True(t, back.app.hasher.Verify(now, "correct-horse-9"))
		})
	}
}
