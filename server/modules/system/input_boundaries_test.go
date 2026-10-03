package system_test

// 规范 §13.2 第 183 条（D-099）：名称类字段不接受不可见字符；日志查询的时间参数超出范围不出 500；
// 命令行重置密码要求账号名和输入逐字相同。

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/modules/system"
)

func TestInput_183_NamesRejectInvisibleCharacters(t *testing.T) {
	f := newFixture(t)
	admin, _ := f.admin("root")
	// 零宽空格、从右到左覆盖符、换行、字节顺序标记
	bad := []string{"审计\u200b员", "audit\u202eor", "审计\n员", "\ufeff审计员"}

	for _, name := range bad {
		r := f.do(admin, "POST", "/system/roles", gin.H{"code": "auditor", "name": name})
		require.Equal(t, httpx.CodeValidation, r.env.Code, "%q: %s", name, r.rec.Body.String())
		require.Contains(t, r.rec.Body.String(), "org.textChars", name)
	}
	roleID := f.createRole(admin, "auditor", nil)
	for _, name := range bad {
		r := f.do(admin, "PUT", fmt.Sprintf("/system/roles/%d", roleID), gin.H{"name": name})
		require.Equal(t, httpx.CodeValidation, r.env.Code, "%q: %s", name, r.rec.Body.String())
	}
	var roleName string
	require.NoError(t, f.app.Deps().DB.Raw("SELECT name FROM ga_role WHERE id = ?", roleID).Scan(&roleName).Error)
	require.Equal(t, "auditor", roleName, "被拒绝的改名没有写进去")

	// 字典名称和各语言文字、字典项的显示文字和各语言文字
	for _, name := range bad {
		r := f.do(admin, "POST", "/system/dicts", gin.H{"code": "level", "name": name})
		require.Equal(t, httpx.CodeValidation, r.env.Code, "%q: %s", name, r.rec.Body.String())
		r = f.do(admin, "POST", "/system/dicts", gin.H{"code": "level", "name": "等级", "nameI18n": gin.H{"en-US": name}})
		require.Equal(t, httpx.CodeValidation, r.env.Code, "%q: %s", name, r.rec.Body.String())
	}
	d := f.do(admin, "POST", "/system/dicts", gin.H{"code": "level", "name": "等级", "nameI18n": gin.H{"en-US": "Level"}})
	require.Equal(t, 0, d.env.Code, d.rec.Body.String())
	dictID := uint64(d.data()["id"].(float64))
	items := fmt.Sprintf("/system/dicts/%d/items", dictID)
	for _, label := range bad {
		r := f.do(admin, "POST", items, gin.H{"value": "1", "label": label})
		require.Equal(t, httpx.CodeValidation, r.env.Code, "%q: %s", label, r.rec.Body.String())
		r = f.do(admin, "POST", items, gin.H{"value": "1", "label": "低", "labelI18n": gin.H{"en-US": label}})
		require.Equal(t, httpx.CodeValidation, r.env.Code, "%q: %s", label, r.rec.Body.String())
	}
	ok := f.do(admin, "POST", items, gin.H{"value": "1", "label": "低", "labelI18n": gin.H{"en-US": "Low"}, "extra": "第一行\n第二行"})
	require.Equal(t, 0, ok.env.Code, ok.rec.Body.String())
	// 零宽连接符、零宽非连接符是孟加拉语、泰米尔语等文字的正常拼写，不算不可见字符
	ok = f.do(admin, "POST", items, gin.H{"value": "2", "label": "র\u200d্যাঙ্ক", "labelI18n": gin.H{"bn-BD": "র\u200d্যাঙ্ক", "en-US": "Rank\u200c"}})
	require.Equal(t, 0, ok.env.Code, ok.rec.Body.String())
	var n int64
	require.NoError(t, f.app.Deps().DB.Raw("SELECT COUNT(*) FROM ga_dict_item WHERE dict_id = ?", dictID).Scan(&n).Error)
	require.EqualValues(t, 2, n)
}

func TestInput_183_OutOfRangeTimeFiltersAreIgnored(t *testing.T) {
	f := newFixture(t)
	admin, _ := f.admin("root")
	// 合法的 RFC 3339 写法，换算成 UTC 后年份超出 1–9999
	early := url.QueryEscape("0000-01-01T00:00:00+23:59")
	late := url.QueryEscape("9999-12-31T23:59:59-23:59")
	for _, path := range []string{"/system/operation-logs", "/system/login-logs", "/system/security-events", "/system/error-logs"} {
		for _, q := range []string{"?from=" + early, "?to=" + late, "?from=" + early + "&to=" + late} {
			r := f.do(admin, "GET", path+q, nil)
			require.Equal(t, 200, r.rec.Code, "%s%s: %s", path, q, r.rec.Body.String())
			require.Equal(t, 0, r.env.Code, "%s%s: %s", path, q, r.rec.Body.String())
		}
	}
	// 时间线的游标：超出范围和格式不对的一样回 3001
	for _, cursor := range []string{early + ",login,1", late + ",login,1"} {
		r := f.do(admin, "GET", "/system/audit/timeline?userId=1&cursor="+cursor, nil)
		require.Equal(t, 200, r.rec.Code, r.rec.Body.String())
		require.Equal(t, httpx.CodeValidation, r.env.Code, r.rec.Body.String())
	}
	var errs int64
	require.NoError(t, f.app.Deps().DB.Raw("SELECT COUNT(*) FROM ga_error_log").Scan(&errs).Error)
	require.Zero(t, errs, "没有产生服务端错误记录")
}

func TestInput_183_CLIResetRequiresExactUsername(t *testing.T) {
	f := newFixture(t)
	f.admin("admin")
	ctx := f.app.Context(context.Background())
	var before string
	require.NoError(t, f.app.Deps().DB.Raw("SELECT password_hash FROM ga_user WHERE username = 'admin'").Scan(&before).Error)

	// 用户表不区分重音：带重音的写法查得到 admin，但不能据此重置它
	for _, alias := range []string{"ádmin", "admín", "\uff41dmin"} {
		_, err := system.ResetPasswordByCLI(ctx, f.app.Deps(), alias, "")
		require.Error(t, err, alias)
		require.Contains(t, err.Error(), "不存在", alias)
	}
	var after string
	require.NoError(t, f.app.Deps().DB.Raw("SELECT password_hash FROM ga_user WHERE username = 'admin'").Scan(&after).Error)
	require.Equal(t, before, after, "别的写法不能重置这个账号")
	f.login("admin", "changed-pass-9")

	// 大小写和首尾空白照旧归一化
	res, err := system.ResetPasswordByCLI(ctx, f.app.Deps(), "  Admin ", "")
	require.NoError(t, err)
	require.NotEmpty(t, res.Password)
}

// 181（D-097）：下线接口只认规范写法的会话号。大写写法在库里匹配得到同一行，但接口回 404、会话不动。
func TestSession_181_NonCanonicalSIDRouteIs404(t *testing.T) {
	f := newFixture(t)
	admin, _ := f.admin("root")
	uid, tok := f.createUser(admin, "bob", nil)
	var sid string
	require.NoError(t, f.app.Deps().DB.Raw("SELECT sid FROM ga_session WHERE user_id = ? AND revoked_at IS NULL ORDER BY id DESC LIMIT 1", uid).Scan(&sid).Error)
	upper := strings.ToUpper(sid)
	require.NotEqual(t, sid, upper)

	r := f.do(admin, "POST", "/system/sessions/"+upper+"/revoke", nil)
	require.Equal(t, 404, r.rec.Code, r.rec.Body.String())
	var revoked int64
	require.NoError(t, f.app.Deps().DB.Raw("SELECT COUNT(*) FROM ga_session WHERE sid = ? AND revoked_at IS NOT NULL", sid).Scan(&revoked).Error)
	require.Zero(t, revoked)
	require.Equal(t, 0, f.do(tok, "GET", "/auth/me", nil).env.Code)

	r = f.do(admin, "POST", "/system/sessions/"+sid+"/revoke", nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.Equal(t, 401, f.do(tok, "GET", "/auth/me", nil).rec.Code, "规范写法吊销后立即失效")
}
