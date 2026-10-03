package app

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/internal/session"
	"github.com/goalladmin/goalladmin/server/core/ipacl"
)

// 规范 §13.2 第 188 条（D-104）：刷新凭证带一个不轮换的家族密钥。家族密钥对得上、凭证却已经被轮换掉不止一次：
// 出示的人持有过这个会话的凭证，会话整体吊销。只知道会话号、家族密钥是编的：仍然只拒绝、不吊销（D-049）。

func TestAuth_188_FamilySecretRevokesWhenStaleCredentialReturns(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct-horse-9")
	name := "ga_rt_" + testPortal
	_, c0 := f.mustLogin("alice", "correct-horse-9")
	tok0, ok := session.ParseRefreshToken(c0)
	require.True(t, ok)
	require.Len(t, tok0.Family, 64, "登录下发的是三段的凭证")
	// 库里只有家族密钥的哈希
	var stored string
	require.NoError(t, f.app.deps.DB.Raw("SELECT family_hash FROM ga_session WHERE sid = ?", tok0.SID).Scan(&stored).Error)
	require.Len(t, stored, 64)
	require.NotEqual(t, tok0.Family, stored)

	// 另一方拿着同一份凭证连刷两次：c0 落到"当前"和"上一个"之外
	r1 := f.refresh(c0)
	require.Equal(t, 200, r1.rec.Code, r1.rec.Body.String())
	c1 := r1.cookie(name)
	r2 := f.refresh(c1)
	require.Equal(t, 200, r2.rec.Code, r2.rec.Body.String())
	c2 := r2.cookie(name)
	access2, _ := r2.data()["accessToken"].(string)
	tok2, ok := session.ParseRefreshToken(c2)
	require.True(t, ok)
	require.Equal(t, tok0.Family, tok2.Family, "家族密钥不轮换")
	require.NotEqual(t, tok0.Secret, tok2.Secret)
	require.Equal(t, 200, f.ping(access2).rec.Code)

	// 原来的持有人拿 c0 来刷新：会话整体吊销，两边都要重新登录；记 refresh_reuse
	stale := f.refresh(c0)
	require.Equal(t, 401, stale.rec.Code, stale.rec.Body.String())
	require.False(t, stale.setsCookie(name))
	var reason string
	require.NoError(t, f.app.deps.DB.Raw("SELECT revoke_reason FROM ga_session WHERE sid = ? AND revoked_at IS NOT NULL", tok0.SID).Scan(&reason).Error)
	require.Equal(t, "reuse_detected", reason)
	require.Equal(t, 401, f.refresh(c2).rec.Code, "另一方手里最新的凭证也不再可用")
	require.Equal(t, 401, f.ping(access2).rec.Code)
	ev := f.securityEvents()
	_, found := ev["refresh_reuse"]
	require.True(t, found, "%v", ev)
	_, mismatch := ev["refresh_mismatch"]
	require.False(t, mismatch)
}

func TestAuth_188_ForgedFamilyOnlyRejects(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct-horse-9")
	name := "ga_rt_" + testPortal
	access, c0 := f.mustLogin("alice", "correct-horse-9")
	c1 := f.refresh(c0).cookie(name)
	r2 := f.refresh(c1)
	require.Equal(t, 200, r2.rec.Code)
	c2 := r2.cookie(name)
	tok2, ok := session.ParseRefreshToken(c2)
	require.True(t, ok)
	zeros, ones := strings.Repeat("0", 64), strings.Repeat("1", 64)

	// 上一个凭证配了编的家族密钥：按"上一个值"处理（宽限期内 409），和不带家族密钥时一样
	tok1, ok := session.ParseRefreshToken(c1)
	require.True(t, ok)
	require.Equal(t, 409, f.refresh(tok1.SID+"."+zeros+"."+tok1.Secret).rec.Code)

	for _, forged := range []string{
		tok2.SID + "." + zeros + "." + ones,        // 家族密钥、凭证都是编的
		tok2.SID + "." + ones,                      // 两段、凭证是编的
		tok2.SID + "." + zeros + "." + tok2.Secret, // 当前的凭证配了编的家族密钥
		tok2.SID + "." + tok2.Secret,               // 当前的凭证不带家族密钥（当前值只随三段的 Cookie 下发）
	} {
		r := f.refresh(forged)
		require.Equal(t, 401, r.rec.Code, forged)
		require.False(t, r.setsCookie(name), forged)
	}
	require.Equal(t, 200, f.ping(access).rec.Code, "会话没有被吊销")
	var revoked int64
	require.NoError(t, f.app.deps.DB.Raw("SELECT COUNT(*) FROM ga_session WHERE sid = ? AND revoked_at IS NOT NULL", tok2.SID).Scan(&revoked).Error)
	require.Zero(t, revoked)
	ok3 := f.refresh(c2)
	require.Equal(t, 200, ok3.rec.Code, "合法持有人照常轮换")
	ev := f.securityEvents()
	m, found := ev["refresh_mismatch"]
	require.True(t, found, "%v", ev)
	require.EqualValues(t, 4, m.Count)
	_, reuse := ev["refresh_reuse"]
	require.False(t, reuse, "编出来的家族密钥不算重放")
}

// 迁移之前建的会话没有家族密钥，手里是两段的凭证：照常能刷新，第一次成功刷新时补上并换发三段的凭证，不用重新登录。
func TestAuth_188_SessionsWithoutFamilyAreUpgradedOnRefresh(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct-horse-9")
	name := "ga_rt_" + testPortal
	_, c0 := f.mustLogin("alice", "correct-horse-9")
	tok0, ok := session.ParseRefreshToken(c0)
	require.True(t, ok)
	require.NoError(t, f.app.deps.DB.Exec("UPDATE ga_session SET family_hash = NULL WHERE sid = ?", tok0.SID).Error)
	legacy := tok0.SID + "." + tok0.Secret

	r1 := f.refresh(legacy)
	require.Equal(t, 200, r1.rec.Code, r1.rec.Body.String())
	c1 := r1.cookie(name)
	tok1, ok := session.ParseRefreshToken(c1)
	require.True(t, ok)
	require.Len(t, tok1.Family, 64, "补上了家族密钥，换发三段的凭证")
	require.NotEqual(t, tok0.Family, tok1.Family, "是新生成的")
	var stored *string
	require.NoError(t, f.app.deps.DB.Raw("SELECT family_hash FROM ga_session WHERE sid = ?", tok0.SID).Scan(&stored).Error)
	require.NotNil(t, stored)
	require.Len(t, *stored, 64)
	access1, _ := r1.data()["accessToken"].(string)
	require.Equal(t, 200, f.ping(access1).rec.Code)

	// 升级那一刻在途的两段凭证（上一个值）：宽限期内是 409，带着新 Cookie 重试，会话不吊销
	require.Equal(t, 409, f.refresh(legacy).rec.Code)

	c2 := f.refresh(c1).cookie(name)
	c3 := f.refresh(c2).cookie(name)
	require.NotEmpty(t, c3)
	// 两段的旧凭证落到两代之外：它没有家族密钥，和以前一样只拒绝、不吊销
	require.Equal(t, 401, f.refresh(legacy).rec.Code)
	r4 := f.refresh(c3)
	require.Equal(t, 200, r4.rec.Code, "会话还在")
	c4 := r4.cookie(name)

	// 升级之后的三段凭证落到两代之外：吊销
	require.Equal(t, 401, f.refresh(c1).rec.Code)
	require.Equal(t, 401, f.refresh(c4).rec.Code, "会话已经整体吊销")
}

// 会导致吊销的旧凭证也排在账号和 IP 白名单的检查之后：名单外的来源拿着它来，得到的是 2003，会话不吊销
// （规范 §13.2 第 124 条）；名单内的来源拿着它来才吊销。
func TestAuth_188_StaleCredentialFromDeniedSourceDoesNotRevoke(t *testing.T) {
	f := newAuthFixture(t)
	id := f.addUser("alice", "correct-horse-9")
	name := "ga_rt_" + testPortal
	_, c0 := f.mustLogin("alice", "correct-horse-9")
	tok0, ok := session.ParseRefreshToken(c0)
	require.True(t, ok)
	c1 := f.refresh(c0).cookie(name)
	r2 := f.refresh(c1)
	require.Equal(t, 200, r2.rec.Code)
	c2 := r2.cookie(name)
	_, err := f.app.deps.IPACL.SetAllow(f.testCtx(), ipacl.UserTarget(testPortal, 0, id), []ipacl.Entry{{CIDR: "203.0.113.10"}}, 1, "")
	require.NoError(t, err)

	requireIPDenied(t, f.refresh(c0, fromIP("198.51.100.1")))
	var revoked int64
	require.NoError(t, f.app.deps.DB.Raw("SELECT COUNT(*) FROM ga_session WHERE sid = ? AND revoked_at IS NOT NULL", tok0.SID).Scan(&revoked).Error)
	require.Zero(t, revoked, "名单外的来源不能让会话吊销")
	require.Zero(t, f.countSecurity("refresh_reuse"))

	require.Equal(t, 401, f.refresh(c0).rec.Code)
	require.NoError(t, f.app.deps.DB.Raw("SELECT COUNT(*) FROM ga_session WHERE sid = ? AND revoked_at IS NOT NULL", tok0.SID).Scan(&revoked).Error)
	require.EqualValues(t, 1, revoked, "名单内的来源出示被轮换掉两次的凭证：吊销")
	require.Equal(t, 401, f.refresh(c2).rec.Code)
}
