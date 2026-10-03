package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/org"
)

// 主体参数守卫（D-067 第 5 条）：主体端的已登录请求里，查询串、JSON 请求体（任意一层的键）、表单字段（包括 multipart 的字段名）
// 出现 orgId、agentId、merchantId（不分大小写，忽略 '_'、'-'、'.' 和方括号，复数 ...Ids 也算）一律回 3002。主体只从会话来（D-061），
// 请求里带了说明调用方以为能指定主体，直接报错，不静默忽略。由 resolveGuard 装在主体端每条非公开路由的守卫链后面。

// orgParamMaxDepth 是 JSON 请求体往下查的最大层数；更深的层没有接口会去绑定。
const orgParamMaxDepth = 32

// orgParamNames 返回不允许出现的参数名（已去掉 '_'、'-'，比较时不分大小写）：主体，以及每种主体端的代号（内置的两种和已注册的主体端）。
func (a *App) orgParamNames() []string {
	bases := []string{"org", org.Agent().Portal(), org.Merchant().Portal()}
	for _, p := range a.deps.Portals.All() {
		if p.Scoped {
			bases = append(bases, p.Code)
		}
	}
	out := make([]string, 0, 2*len(bases))
	for _, b := range bases {
		b = stripParamSeparators(b)
		out = append(out, b+"id", b+"ids")
	}
	return out
}

// stripParamSeparators 去掉参数名里的 '_'、'-'、'.' 和方括号：org_id、org-id、orgId 是同一个意思，
// orgId[]、org[id] 是 gin 按数组、按 map 绑定时的写法。
func stripParamSeparators(s string) string {
	return strings.NewReplacer("_", "", "-", "", ".", "", "[", "", "]", "").Replace(s)
}

// reservedParam 报告 name 是不是不允许出现的参数名。用 EqualFold 比较：encoding/json 按 Unicode 的大小写折叠匹配字段名
// （开尔文符号 K 和 k、长 s ſ 和 s 算同一个字母），这里要和它一样宽，不然换个写法就能绑进结构体。
func reservedParam(names []string, name string) bool {
	name = stripParamSeparators(name)
	for _, n := range names {
		if strings.EqualFold(name, n) {
			return true
		}
	}
	return false
}

// orgParamGuard 返回主体参数守卫中间件。
func (a *App) orgParamGuard() gin.HandlerFunc {
	names := a.orgParamNames()
	return func(c *gin.Context) {
		if name, ok := findQueryParam(names, c.Request.URL.RawQuery); ok {
			rejectOrgParam(c, name)
			return
		}
		name, ok, err := findBodyParam(c, names)
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				httpx.Fail(c, httpx.ErrBodyTooLarge.WithCause(err))
				return
			}
			httpx.Fail(c, httpx.ErrBadRequest.WithCause(err))
			return
		}
		if ok {
			rejectOrgParam(c, name)
			return
		}
		c.Next()
	}
}

func rejectOrgParam(c *gin.Context, name string) {
	httpx.Fail(c, httpx.NewKey(httpx.CodeBadRequest, "scope.orgParam",
		"the org comes from the login session; requests must not carry {param}", "param", name))
}

// findQueryParam 在查询串里找不允许的参数名。解析不了的片段和 gin 一样跳过（它们也绑不进任何参数）。
func findQueryParam(names []string, raw string) (string, bool) {
	if raw == "" {
		return "", false
	}
	q, _ := url.ParseQuery(raw)
	for k := range q {
		if reservedParam(names, k) {
			return k, true
		}
	}
	return "", false
}

// findBodyParam 读出请求体（已经被 BodyLimit 限过长度）找不允许的参数名，再把请求体原样放回去给后面的处理函数。
// 表单、multipart 按字段名查；不论 Content-Type 写的是什么，请求体都再按 JSON 查一次：只按类型挑一种解析的话，
// 换个类型就能绕过去。httpx.BindJSON 从 D-095 起只收声明成 JSON 的请求体，这里仍然不看类型，作为第二道检查保留
// （业务模块自己读请求体的处理函数不经过 BindJSON）。
// 解析不了的请求体不在这里报错，留给处理函数按原来的规则回 3002。
func findBodyParam(c *gin.Context, names []string) (string, bool, error) {
	if c.Request.Body == nil || c.Request.Body == http.NoBody {
		return "", false, nil
	}
	mt, params, _ := mime.ParseMediaType(c.GetHeader("Content-Type"))
	buf, err := io.ReadAll(c.Request.Body)
	_ = c.Request.Body.Close()
	c.Request.Body = io.NopCloser(bytes.NewReader(buf))
	if err != nil {
		return "", false, err
	}
	switch mt {
	case "application/x-www-form-urlencoded":
		if name, ok := findQueryParam(names, string(buf)); ok {
			return name, true, nil
		}
	case "multipart/form-data":
		if name, ok := findMultipartParam(names, buf, params["boundary"]); ok {
			return name, true, nil
		}
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(buf))
	dec.UseNumber()
	if dec.Decode(&v) != nil {
		return "", false, nil
	}
	name, ok := findJSONKey(names, v, 0)
	return name, ok, nil
}

// findMultipartParam 在 multipart 请求体的字段名里找不允许的参数名；没有分隔符、解析出错时就此停下。
func findMultipartParam(names []string, buf []byte, boundary string) (string, bool) {
	if boundary == "" {
		return "", false
	}
	r := multipart.NewReader(bytes.NewReader(buf), boundary)
	for {
		part, err := r.NextPart()
		if err != nil {
			return "", false
		}
		name := part.FormName()
		_ = part.Close()
		if reservedParam(names, name) {
			return name, true
		}
	}
}

// findJSONKey 在 JSON 值的任意一层对象里找不允许的键。
func findJSONKey(names []string, v any, depth int) (string, bool) {
	if depth > orgParamMaxDepth {
		return "", false
	}
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if reservedParam(names, k) {
				return k, true
			}
			if name, ok := findJSONKey(names, child, depth+1); ok {
				return name, true
			}
		}
	case []any:
		for _, child := range t {
			if name, ok := findJSONKey(names, child, depth+1); ok {
				return name, true
			}
		}
	}
	return "", false
}
