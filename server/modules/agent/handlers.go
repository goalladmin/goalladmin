package agent

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/goalladmin/goalladmin/server/core/app"
	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/ipacl"
	"github.com/goalladmin/goalladmin/server/core/oplog"
	"github.com/goalladmin/goalladmin/server/core/org"
	"github.com/goalladmin/goalladmin/server/core/portal"
)

type handlers struct {
	deps *app.Deps
	kind org.Kind
}

// withActor 拿超管锁、按库重新认定操作人（含路由要求的权限码），再执行 fn（D-047、D-048）。
func (h *handlers) withActor(c *gin.Context, fn func(ctx context.Context, actor auth.Principal) error) error {
	ctx := c.Request.Context()
	return h.deps.RBAC.WithActor(ctx, auth.MustFromCtx(ctx), fn)
}

func pathID(c *gin.Context) (uint64, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		httpx.Fail(c, httpx.ErrNotFound)
		return 0, false
	}
	return id, true
}

func queryStatus(c *gin.Context) *int {
	v, err := strconv.Atoi(c.Query("status"))
	if err != nil {
		return nil
	}
	return &v
}

func (h *handlers) list(c *gin.Context) {
	q := httpx.BindPage(c)
	list, total, err := h.deps.Orgs.List(c.Request.Context(), h.kind, org.Filter{Status: queryStatus(c)}, q)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OKPage(c, list, total, q.Page, q.PageSize)
}

func (h *handlers) get(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	it, err := h.deps.Orgs.Get(c.Request.Context(), h.kind, id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, it)
}

type createRequest struct {
	Name             string `json:"name"`
	ContactName      string `json:"contactName"`
	ContactPhone     string `json:"contactPhone"`
	Sort             uint   `json:"sort"`
	Remark           string `json:"remark"`
	OwnerUsername    string `json:"ownerUsername"`
	OwnerDisplayName string `json:"ownerDisplayName"`
}

// create 开代理商，同时建主账号。主账号的初始密码只在这次响应里出现（操作日志不记响应体）。
func (h *handlers) create(c *gin.Context) {
	var req createRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	in := org.CreateInput{
		Name: req.Name, ContactName: req.ContactName, ContactPhone: req.ContactPhone, Sort: req.Sort, Remark: req.Remark,
		OwnerUsername: req.OwnerUsername, OwnerDisplayName: req.OwnerDisplayName,
	}
	// 字段校验先做（D-068）：不合法的请求不去算哈希
	if err := h.deps.Orgs.CheckCreate(h.kind, in); err != nil {
		httpx.Fail(c, err)
		return
	}
	// 密码哈希慢：在拿锁之前算好（D-066），和登录核对密码占同一个并发上限（D-068）
	pwd, err := h.deps.Orgs.NewInitialPassword()
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var created *org.Created
	err = h.withActor(c, func(ctx context.Context, actor auth.Principal) error {
		var err error
		created, err = h.deps.Orgs.Create(ctx, h.kind, in, pwd, actor.UserID)
		return err
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"agent": created.Org, "ownerId": created.OwnerID, "initialPassword": created.Password})
}

type updateRequest struct {
	Name         string `json:"name"`
	ContactName  string `json:"contactName"`
	ContactPhone string `json:"contactPhone"`
	Sort         uint   `json:"sort"`
	Remark       string `json:"remark"`
}

func (h *handlers) update(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req updateRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	err := h.withActor(c, func(ctx context.Context, actor auth.Principal) error {
		return h.deps.Orgs.Update(ctx, h.kind, id, org.UpdateInput(req), actor.UserID)
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nil)
}

type statusRequest struct {
	Status *int `json:"status" binding:"required"`
}

// setStatus 启停代理商。停用时同一个事务里吊销这个代理商的全部会话；名下商户不受影响。
func (h *handlers) setStatus(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req statusRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	err := h.withActor(c, func(ctx context.Context, actor auth.Principal) error {
		return h.deps.Orgs.SetStatus(ctx, h.kind, id, *req.Status, actor.UserID)
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nil)
}

// resetOwnerPassword 把主账号的密码换成随机密码（只在这次响应里出现），吊销主账号的全部会话。
func (h *handlers) resetOwnerPassword(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	// 主体不存在、没有主账号的请求不去算哈希（D-068）；锁内照样认定
	if err := h.deps.Orgs.CheckResetOwner(c.Request.Context(), h.kind, id); err != nil {
		httpx.Fail(c, err)
		return
	}
	pwd, err := h.deps.Orgs.NewInitialPassword() // 在拿锁之前算好哈希
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	err = h.withActor(c, func(ctx context.Context, _ auth.Principal) error {
		return h.deps.Orgs.ResetOwnerPassword(ctx, h.kind, id, pwd)
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"initialPassword": pwd.Plain()})
}

type ownerRequest struct {
	UserID uint64 `json:"userId" binding:"required"`
}

func (h *handlers) changeOwner(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req ownerRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	err := h.withActor(c, func(ctx context.Context, actor auth.Principal) error {
		return h.deps.Orgs.ChangeOwner(ctx, h.kind, id, req.UserID, actor.UserID)
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nil)
}

func (h *handlers) accounts(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	q := httpx.BindPage(c)
	list, total, err := h.deps.Orgs.Accounts(c.Request.Context(), h.kind, id, queryStatus(c), q)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OKPage(c, list, total, q.Page, q.PageSize)
}

func (h *handlers) sessions(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	q := httpx.BindPage(c)
	list, total, err := h.deps.Orgs.Sessions(c.Request.Context(), h.kind, id, q)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OKPage(c, list, total, q.Page, q.PageSize)
}

func (h *handlers) revokeSession(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	err := h.withActor(c, func(ctx context.Context, _ auth.Principal) error {
		return h.deps.Orgs.RevokeSession(ctx, h.kind, id, c.Param("sid"))
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nil)
}

// ipAllow 查看代理商的 IP 白名单（代理商的主账号在代理商端自己设，D-062）。
func (h *handlers) ipAllow(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if _, err := h.deps.Orgs.Get(ctx, h.kind, id); err != nil {
		httpx.Fail(c, err)
		return
	}
	rules, err := h.deps.IPACL.ListAllow(ctx, ipacl.OrgTarget(h.kind.Portal(), id))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"items": rules})
}

// clearIPAllow 清空代理商的 IP 白名单（代理商把自己挡在外面时由平台解除）。
func (h *handlers) clearIPAllow(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var n int64
	err := h.withActor(c, func(ctx context.Context, _ auth.Principal) error {
		if _, err := h.deps.Orgs.Get(ctx, h.kind, id); err != nil {
			return err
		}
		var err error
		n, err = h.deps.IPACL.ClearAllow(ctx, ipacl.OrgTarget(h.kind.Portal(), id))
		return err
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"removed": n})
}

// ipDeny 列出代理商自己设的 IP 黑名单，含已过期的记录（D-102）。名单由代理商的主账号在自己的后台设，平台只能看和整体清空。
func (h *handlers) ipDeny(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if _, err := h.deps.Orgs.Get(ctx, h.kind, id); err != nil {
		httpx.Fail(c, err)
		return
	}
	q := httpx.BindPage(c)
	items, total, err := h.deps.IPACL.ListOrgDeny(ctx, ipacl.OrgTarget(h.kind.Portal(), id), ipacl.DenyFilter{Keyword: q.Keyword, IncludeExpired: true}, q.Page, q.PageSize)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OKPage(c, items, total, q.Page, q.PageSize)
}

// clearIPDeny 清空代理商自己设的 IP 黑名单（代理商把接手的人挡在外面时由平台解除）。
func (h *handlers) clearIPDeny(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var n int64
	err := h.withActor(c, func(ctx context.Context, _ auth.Principal) error {
		if _, err := h.deps.Orgs.Get(ctx, h.kind, id); err != nil {
			return err
		}
		var err error
		n, err = h.deps.IPACL.ClearOrgDeny(ctx, ipacl.OrgTarget(h.kind.Portal(), id))
		return err
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"removed": n})
}

// ---- 代理商端的日志（只读）----

// queryTime 解析 RFC 3339 时间参数；空串、格式错误、换算成 UTC 后年份超出 1–9999 的都当作没传（D-099）。
func queryTime(c *gin.Context, key string) time.Time {
	t, _ := httpx.ParseTime(c.Query(key))
	return t
}

func queryBool(c *gin.Context, key string) *bool {
	switch c.Query(key) {
	case "1", "true":
		v := true
		return &v
	case "0", "false":
		v := false
		return &v
	}
	return nil
}

// errNoSuchOrg：按编号筛选时编号不存在，结果为空。
var errNoSuchOrg = errors.New("agent: 编号不存在")

// orgFilter 把 orgCode 参数换成代理商 ID；没传返回 0。
func (h *handlers) orgFilter(c *gin.Context) (uint64, error) {
	code := portal.NormalizeOrgCode(c.Query("orgCode"))
	if code == "" {
		return 0, nil
	}
	o, err := h.deps.Orgs.Users(h.kind).FindOrgByCode(c.Request.Context(), code)
	if errors.Is(err, portal.ErrOrgNotFound) {
		return 0, errNoSuchOrg
	}
	if err != nil {
		return 0, err
	}
	return o.ID, nil
}

// orgNames 取一页日志里涉及的代理商的编号和名称。
func (h *handlers) orgNames(ctx context.Context, ids []uint64) (map[uint64]org.Brief, error) {
	return h.deps.Orgs.Briefs(ctx, h.kind, ids)
}

type loginLogView struct {
	auth.LoginLogInfo
	OrgName string `json:"orgName"`
}

func (h *handlers) loginLogs(c *gin.Context) {
	q := httpx.BindPage(c)
	orgID, err := h.orgFilter(c)
	if errors.Is(err, errNoSuchOrg) {
		httpx.OKPage(c, []loginLogView{}, 0, q.Page, q.PageSize)
		return
	}
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	ctx := c.Request.Context()
	f := auth.LoginLogFilter{
		Portal:    h.kind.Portal(),
		OrgID:     orgID,
		Username:  c.Query("username"),
		IP:        c.Query("ip"),
		SessionID: c.Query("sessionId"),
		Success:   queryBool(c, "success"),
		From:      queryTime(c, "from"),
		To:        queryTime(c, "to"),
	}
	rows, total, err := h.deps.Auth.ListLoginLogs(ctx, f, q.Page, q.PageSize)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	ids := make([]uint64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.OrgID)
	}
	names, err := h.orgNames(ctx, ids)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out := make([]loginLogView, len(rows))
	for i, r := range rows {
		out[i] = loginLogView{LoginLogInfo: r, OrgName: names[r.OrgID].Name}
	}
	httpx.OKPage(c, out, total, q.Page, q.PageSize)
}

type oplogView struct {
	oplog.Entry
	OrgCode string `json:"orgCode"`
	OrgName string `json:"orgName"`
}

func (h *handlers) operationLogs(c *gin.Context) {
	q := httpx.BindPage(c)
	orgID, err := h.orgFilter(c)
	if errors.Is(err, errNoSuchOrg) {
		httpx.OKPage(c, []oplogView{}, 0, q.Page, q.PageSize)
		return
	}
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	ctx := c.Request.Context()
	f := oplog.Filter{
		Portal:    h.kind.Portal(),
		OrgID:     orgID,
		Username:  c.Query("username"),
		Action:    c.Query("action"),
		Path:      c.Query("path"),
		Method:    c.Query("method"),
		IP:        c.Query("ip"),
		SessionID: c.Query("sessionId"),
		Failed:    queryBool(c, "failed"),
		From:      queryTime(c, "from"),
		To:        queryTime(c, "to"),
	}
	rows, total, err := oplog.List(ctx, f, q.Page, q.PageSize)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	ids := make([]uint64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.OrgID)
	}
	names, err := h.orgNames(ctx, ids)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out := make([]oplogView, len(rows))
	for i, r := range rows {
		b := names[r.OrgID]
		out[i] = oplogView{Entry: r, OrgCode: b.Code, OrgName: b.Name}
	}
	httpx.OKPage(c, out, total, q.Page, q.PageSize)
}
