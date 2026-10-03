package app

// 规范 §13.2 第 40–41 条：锁屏在服务端生效（D-027）。刷新页面、换新的访问令牌、直接调接口都绕不过锁屏；
// 解锁要当前账号的密码，连续输错到上限就吊销会话。

import (
	"strconv"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/internal/authimpl"
)

// ============ 40. 锁定后只能查看自己、解锁、登出；刷新换来的令牌照样锁着 ============

func TestLock_40_EnforcedByServer(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "alice-pass-123")
	access, refresh := f.mustLogin("alice", "alice-pass-123")
	other, _ := f.mustLogin("alice", "alice-pass-123") // 同一个人的另一个会话（另一台设备）
	require.Equal(t, 0, f.ping(access).env.Code)

	r := f.do("POST", "/auth/lock", nil, bearerOpt(access))
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())

	// 业务接口：423 / 1006
	r = f.ping(access)
	require.Equal(t, 423, r.rec.Code)
	require.Equal(t, httpx.CodeSessionLocked, r.env.Code)
	// 改密也不行（锁屏不能拿来绕过"先输原密码"之外的任何检查）
	r = f.do("PUT", "/auth/password", gin.H{"oldPassword": "alice-pass-123", "newPassword": "another-pass-456"}, bearerOpt(access))
	require.Equal(t, httpx.CodeSessionLocked, r.env.Code)
	// /auth/me 只给用户名，不给权限和菜单
	r = f.do("GET", "/auth/me", nil, bearerOpt(access))
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.Equal(t, true, r.data()["locked"])
	require.Empty(t, r.data()["perms"])
	require.Empty(t, r.data()["menus"])
	require.Equal(t, "alice", r.data()["user"].(map[string]any)["username"])

	// 刷新页面：刷新凭证换来新的访问令牌，但会话还是锁着
	rr := f.refresh(refresh)
	require.Equal(t, 0, rr.env.Code, rr.rec.Body.String())
	fresh, _ := rr.data()["accessToken"].(string)
	require.Equal(t, httpx.CodeSessionLocked, f.ping(fresh).env.Code)

	// 同一个人的另一个会话不受影响
	require.Equal(t, 0, f.ping(other).env.Code)

	// 输错密码：3001，告诉还剩几次
	r = f.do("POST", "/auth/unlock", gin.H{"password": "wrong"}, bearerOpt(fresh))
	require.Equal(t, httpx.CodeValidation, r.env.Code)
	require.Contains(t, r.rec.Body.String(), `"key":"password.unlockIncorrect","params":{"left":4}`)
	require.Equal(t, httpx.CodeSessionLocked, f.ping(fresh).env.Code)

	// 输对：解锁，业务接口恢复
	r = f.do("POST", "/auth/unlock", gin.H{"password": "alice-pass-123"}, bearerOpt(fresh))
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.Equal(t, 0, f.ping(fresh).env.Code)
	r = f.do("GET", "/auth/me", nil, bearerOpt(fresh))
	require.Equal(t, false, r.data()["locked"])

	// 再锁、再解：失败次数从零算起
	require.Equal(t, 0, f.do("POST", "/auth/lock", nil, bearerOpt(fresh)).env.Code)
	for i := range authimpl.MaxUnlockFailures - 1 {
		r = f.do("POST", "/auth/unlock", gin.H{"password": "wrong"}, bearerOpt(fresh))
		require.Equal(t, httpx.CodeValidation, r.env.Code, "第 %d 次", i+1)
	}
	require.Equal(t, 0, f.do("POST", "/auth/unlock", gin.H{"password": "alice-pass-123"}, bearerOpt(fresh)).env.Code)

	// 锁定和解锁都记了操作日志，请求体里的密码打码
	var bodies []string
	require.NoError(t, f.app.Deps().DB.Raw("SELECT body FROM ga_operation_log WHERE action = ? ORDER BY id", OpUnlock).Scan(&bodies).Error)
	require.NotEmpty(t, bodies)
	for _, b := range bodies {
		require.NotContains(t, b, "alice-pass-123")
		require.NotContains(t, b, "wrong")
	}
	var locks int64
	require.NoError(t, f.app.Deps().DB.Raw("SELECT COUNT(*) FROM ga_operation_log WHERE action = ?", OpLock).Scan(&locks).Error)
	require.EqualValues(t, 2, locks)

	// 锁定状态下可以登出
	require.Equal(t, 0, f.do("POST", "/auth/lock", nil, bearerOpt(fresh)).env.Code)
	r = f.do("POST", "/auth/logout", nil, bearerOpt(fresh), webClient())
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.Equal(t, 401, f.ping(fresh).rec.Code)
}

// ============ 41. 连续输错到上限吊销会话，刷新凭证也作废 ============

func TestLock_41_TooManyWrongPasswordsRevokesSession(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("bob", "bob-pass-123")
	access, refresh := f.mustLogin("bob", "bob-pass-123")
	require.Equal(t, 0, f.do("POST", "/auth/lock", nil, bearerOpt(access)).env.Code)

	var r resp
	for i := range authimpl.MaxUnlockFailures {
		r = f.do("POST", "/auth/unlock", gin.H{"password": "guess-" + string(rune('a'+i))}, bearerOpt(access))
	}
	require.Equal(t, 401, r.rec.Code, "第 %d 次输错吊销会话", authimpl.MaxUnlockFailures)
	require.Equal(t, 401, f.ping(access).rec.Code)
	// 正确的密码也来不及了：会话已经没了
	require.Equal(t, 401, f.do("POST", "/auth/unlock", gin.H{"password": "bob-pass-123"}, bearerOpt(access)).rec.Code)
	// 刷新凭证作废，只能重新登录；解锁时输错的次数计入登录防护，所以重新登录要验证码
	require.Equal(t, 401, f.refresh(refresh).rec.Code)
	require.Equal(t, httpx.CodeCaptchaRequired, f.login("bob", "bob-pass-123").env.Code)
	cp := f.do("GET", "/auth/captcha", nil)
	cid, _ := cp.data()["captchaId"].(string)
	r = f.do("POST", "/auth/login", gin.H{"username": "bob", "password": "bob-pass-123", "captchaId": cid, "captchaCode": f.app.captcha.Peek(cid)})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
}

// 并发的解锁请求也只有 MaxUnlockFailures 次机会：机会在校验密码之前就占掉，
// 不会出现一批请求都通过检查、各自去试密码的情况（用完之后的请求连密码都不校验）。
func TestLock_41_ConcurrentGuessesShareTheLimit(t *testing.T) {
	// 核对密码的并发上限（D-058）放宽到比并发数大：这里测的是解锁次数，不是那道闸门
	f := newAuthFixture(t, func(a *App) { a.pwdParallel = 100 })
	uid := f.addUser("carol", "carol-pass-123")
	access, _ := f.mustLogin("carol", "carol-pass-123")
	require.Equal(t, 0, f.do("POST", "/auth/lock", nil, bearerOpt(access)).env.Code)

	const n = 30
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = f.do("POST", "/auth/unlock", gin.H{"password": "guess-" + strconv.Itoa(i)}, bearerOpt(access)).env.Code
		}()
	}
	wg.Wait()

	checked := 0
	for _, c := range codes {
		if c == httpx.CodeValidation {
			checked++
		} else {
			require.Equal(t, httpx.CodeTokenInvalid, c)
		}
	}
	// 前 Max-1 次输错提示剩余次数，第 Max 次吊销；之后的请求都是 401
	require.Equal(t, authimpl.MaxUnlockFailures-1, checked)
	var failures uint
	require.NoError(t, f.app.Deps().DB.Raw("SELECT unlock_failures FROM ga_session WHERE portal = ? AND user_id = ?", testPortal, uid).Scan(&failures).Error)
	require.EqualValues(t, authimpl.MaxUnlockFailures, failures)
	// 正确的密码也不能再解锁
	require.Equal(t, 401, f.do("POST", "/auth/unlock", gin.H{"password": "carol-pass-123"}, bearerOpt(access)).rec.Code)
}

// 规范 §13.2 第 92 条：解锁核对密码之后、解除锁定之前密码被改了（同一会话在途的改密、管理员重置）：刚输入的已经不是
// 当前密码，按输错处理，会话仍然锁着（D-053）。解锁时锁住账号行再确认一次，和改密同一个加锁顺序。
func TestLock_92_UnlockRechecksPasswordUnderLock(t *testing.T) {
	f := newAuthFixture(t)
	id := f.addUser("alice", "alice-pass-123")
	access, _ := f.mustLogin("alice", "alice-pass-123")
	require.Equal(t, 0, f.do("POST", "/auth/lock", nil, bearerOpt(access)).env.Code)

	newHash := f.hashOf("changed-pass-456")
	// 解锁读到账号、核对旧密码之后，改密提交了
	f.users.mu.Lock()
	f.users.afterFind = func() { f.users.byID[id].PasswordHash = newHash }
	f.users.mu.Unlock()
	r := f.do("POST", "/auth/unlock", gin.H{"password": "alice-pass-123"}, bearerOpt(access))
	require.Equal(t, httpx.CodeValidation, r.env.Code, r.rec.Body.String())
	require.Contains(t, r.rec.Body.String(), `"key":"password.unlockIncorrect"`)
	require.Equal(t, httpx.CodeSessionLocked, f.ping(access).env.Code, "会话仍然锁着")

	// 用现在的密码照常解锁
	require.Equal(t, 0, f.do("POST", "/auth/unlock", gin.H{"password": "changed-pass-456"}, bearerOpt(access)).env.Code)
	require.Equal(t, 0, f.ping(access).env.Code)
}
