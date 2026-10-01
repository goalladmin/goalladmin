package system

// 部门与岗位（docs/decisions.md D-033）。部门本身不带任何权限；按部门的数据权限（D-039）把它当作
// "本部门""本部门及下级"的参照点，由 orgProvider 提供给内核。

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/goalladmin/goalladmin/server/core/app"
	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/portal"
	"github.com/goalladmin/goalladmin/server/core/rbac"
)

// 组织结构的上限（D-033）。
const (
	maxDeptDepth = 10 // 部门最多 10 层
	maxUserPosts = 20 // 一个用户最多 20 个岗位
)

// Dept 对应 ga_dept。
type Dept struct {
	ID           uint64    `gorm:"column:id;primaryKey" json:"id"`
	ParentID     uint64    `gorm:"column:parent_id" json:"parentId"`
	Name         string    `gorm:"column:name" json:"name"`
	LeaderUserID uint64    `gorm:"column:leader_user_id" json:"leaderUserId"`
	Phone        string    `gorm:"column:phone" json:"phone"`
	Email        string    `gorm:"column:email" json:"email"`
	Status       int       `gorm:"column:status" json:"status"`
	Sort         uint      `gorm:"column:sort" json:"sort"`
	Remark       string    `gorm:"column:remark" json:"remark"`
	CreatedAt    time.Time `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt    time.Time `gorm:"column:updated_at" json:"updatedAt"`
	CreatedBy    uint64    `gorm:"column:created_by" json:"-"`
	UpdatedBy    uint64    `gorm:"column:updated_by" json:"-"`
}

// TableName 固定表名。
func (Dept) TableName() string { return "ga_dept" }

// Post 对应 ga_post。
type Post struct {
	ID        uint64    `gorm:"column:id;primaryKey" json:"id"`
	Code      string    `gorm:"column:code" json:"code"`
	Name      string    `gorm:"column:name" json:"name"`
	Status    int       `gorm:"column:status" json:"status"`
	Sort      uint      `gorm:"column:sort" json:"sort"`
	Remark    string    `gorm:"column:remark" json:"remark"`
	CreatedAt time.Time `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updatedAt"`
	CreatedBy uint64    `gorm:"column:created_by" json:"-"`
	UpdatedBy uint64    `gorm:"column:updated_by" json:"-"`
}

// TableName 固定表名。
func (Post) TableName() string { return "ga_post" }

type userPost struct {
	UserID uint64 `gorm:"column:user_id;primaryKey"`
	PostID uint64 `gorm:"column:post_id;primaryKey"`
}

func (userPost) TableName() string { return "ga_user_post" }

var postCodeRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// OrgService 是部门、岗位和用户归属的业务逻辑。
type OrgService struct {
	deps  *app.Deps // 改部门上级时查数据范围（D-039）；deps.RBAC 在 Routes 之后才有
	users *UserRepo
}

// NewOrgService 创建服务。
func NewOrgService(deps *app.Deps, users *UserRepo) *OrgService {
	return &OrgService{deps: deps, users: users}
}

// orgProvider 把组织结构提供给内核，按部门的数据权限用（D-039）。每次现查，调部门立即生效。
type orgProvider struct{ s *OrgService }

func (p orgProvider) UserDept(ctx context.Context, userID uint64) (uint64, error) {
	u, err := p.s.users.FindByID(ctx, userID)
	if errors.Is(err, portal.ErrAccountNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return u.DeptID, nil
}

func (p orgProvider) DeptSubtree(ctx context.Context, deptID uint64) ([]uint64, error) {
	return p.s.deptSubtree(ctx, deptID)
}

// ---- 校验 ----

// cleanName 去掉首尾空白，校验 1–64 个字符、不含控制字符和不可见的格式字符。
func cleanName(field, s string) (string, error) {
	s = strings.TrimSpace(s)
	n := 0
	for _, r := range s {
		n++
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return "", httpx.ErrValidation.WithFields(httpx.NewField(field, "system.org.nameChars", "must not contain control or invisible characters"))
		}
	}
	if n == 0 || n > 64 {
		return "", httpx.ErrValidation.WithFields(httpx.NewField(field, "system.org.nameLength", "1-64 characters"))
	}
	return s, nil
}

func checkStatus(status *int) (int, error) {
	if status == nil {
		return StatusEnabled, nil
	}
	if *status != StatusEnabled && *status != StatusDisabled {
		return 0, httpx.ErrValidation.WithFields(httpx.NewField("status", "common.status", "must be 0 or 1"))
	}
	return *status, nil
}

// ---- 部门 ----

// DeptView 是部门的管理视图。
type DeptView struct {
	Dept
	LeaderName string `json:"leaderName"`
	UserCount  int64  `json:"userCount"` // 直接属于这个部门的用户数
	// LeaderHidden：部门有负责人，但他不在调用者"查看用户"的范围内（D-055）。这时 leaderUserId 是 0、leaderName 为空——
	// 部门结构和人数谁都能看，具体是哪个人要按用户范围来，否则部门列表能把范围外的人一个个带出来（D-039 第 6 条）。
	LeaderHidden bool `json:"leaderHidden"`
}

// DeptInput 是新建、编辑部门的入参。
type DeptInput struct {
	ParentID     uint64
	Name         string
	LeaderUserID *uint64 // 为 nil：建部门时没有负责人，改部门时保持原来的负责人（调用者可能看不到他是谁，D-055）；0 表示清空
	Phone        string
	Email        string
	Status       *int
	Sort         uint
	Remark       string
}

// ListDepts 返回全部部门（扁平列表，按排序值），带负责人名字和直属用户数。负责人按调用者"查看用户"的范围遮蔽（D-055）：
// 范围、部门、负责人在同一个只读快照里读（D-054）。
func (s *OrgService) ListDepts(ctx context.Context, actor auth.Principal) ([]DeptView, error) {
	var out []DeptView
	err := db.Snapshot(ctx, func(ctx context.Context) error {
		var rows []Dept
		if err := db.From(ctx).Order("sort ASC, id ASC").Find(&rows).Error; err != nil {
			return err
		}
		mask, err := s.leaderMask(ctx, actor, rows)
		if err != nil {
			return err
		}
		var counts []struct {
			DeptID uint64 `gorm:"column:dept_id"`
			N      int64  `gorm:"column:n"`
		}
		if err := db.From(ctx).Raw("SELECT dept_id, COUNT(*) AS n FROM ga_user WHERE dept_id <> 0 GROUP BY dept_id").Scan(&counts).Error; err != nil {
			return err
		}
		byDept := make(map[uint64]int64, len(counts))
		for _, c := range counts {
			byDept[c.DeptID] = c.N
		}
		out = make([]DeptView, 0, len(rows))
		for _, d := range rows {
			out = append(out, mask(DeptView{Dept: d, UserCount: byDept[d.ID]}))
		}
		return nil
	})
	return out, err
}

// leaderMask 返回一个函数：负责人在调用者"查看用户"的范围内时填上名字，不在时遮掉 ID 并标记 LeaderHidden（D-055）。
// 在调用方的只读快照里调用。
func (s *OrgService) leaderMask(ctx context.Context, actor auth.Principal, depts []Dept) (func(DeptView) DeptView, error) {
	f, err := s.deps.RBAC.DataFilter(ctx, actor, DataUser, PermUserList)
	if err != nil {
		return nil, err
	}
	ids := make([]uint64, 0, len(depts))
	for _, d := range depts {
		if d.LeaderUserID != 0 {
			ids = append(ids, d.LeaderUserID)
		}
	}
	users, err := s.users.FindByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	return func(v DeptView) DeptView {
		if v.LeaderUserID == 0 {
			return v
		}
		if u, ok := users[v.LeaderUserID]; ok && f.Allows(u.DeptID, u.ID) {
			v.LeaderName = u.DisplayName
			return v
		}
		v.LeaderUserID, v.LeaderName, v.LeaderHidden = 0, "", true
		return v
	}, nil
}

// deptView 是写操作之后的回显：负责人同样按调用者的查看范围遮蔽（D-055）。
//
// 超管身份按库认定（D-056，同用户的 viewAfterWrite）：不沿用请求开始时的 actor.Super。超管在途中被降级、
// 仍有编辑部门的权限时，旧身份的"全部"会把范围外的负责人带出来；在回显自己的快照里按库算，提交后、回显前
// 再被收窄也照样按收窄后的算。服务器上的命令（UserID 为 0）没有可以重读的角色，身份照调用方给的算。
func (s *OrgService) deptView(ctx context.Context, actor auth.Principal, id uint64) (*DeptView, error) {
	if actor.UserID != 0 {
		actor.Super = false
	}
	var v DeptView
	err := db.Snapshot(ctx, func(ctx context.Context) error {
		var d Dept
		if err := db.From(ctx).Where("id = ?", id).Take(&d).Error; err != nil {
			return err
		}
		mask, err := s.leaderMask(ctx, actor, []Dept{d})
		if err != nil {
			return err
		}
		if err := db.From(ctx).Model(&User{}).Where("dept_id = ?", id).Count(&v.UserCount).Error; err != nil {
			return err
		}
		v = mask(DeptView{Dept: d, UserCount: v.UserCount})
		return nil
	})
	return &v, err
}

// lockDepts 在事务里锁住整张部门表并读出来。改动部门树的操作都先调它，两个管理员同时挪部门也不会成环（D-033）。
func lockDepts(ctx context.Context) (map[uint64]Dept, error) {
	var rows []Dept
	if err := db.From(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[uint64]Dept, len(rows))
	for _, d := range rows {
		out[d.ID] = d
	}
	return out, nil
}

// depthOf 返回部门的层数（顶级为 1）。树里有环时返回一个超过上限的数，调用方据此拒绝。
func depthOf(all map[uint64]Dept, id uint64) int {
	n := 0
	for id != 0 && n <= maxDeptDepth+1 {
		n++
		id = all[id].ParentID
	}
	return n
}

// heightOf 返回以 id 为根的子树的高度（只有自己为 1）。
func heightOf(all map[uint64]Dept, id uint64) int {
	children := map[uint64][]uint64{}
	for _, d := range all {
		children[d.ParentID] = append(children[d.ParentID], d.ID)
	}
	var walk func(uint64, int) int
	walk = func(n uint64, level int) int {
		best := level
		if level > maxDeptDepth+1 {
			return level
		}
		for _, c := range children[n] {
			if h := walk(c, level+1); h > best {
				best = h
			}
		}
		return best
	}
	return walk(id, 1)
}

// isDescendant 报告 candidate 是否是 root 自己或它的下级。
func isDescendant(all map[uint64]Dept, root, candidate uint64) bool {
	for i := 0; candidate != 0 && i <= maxDeptDepth+1; i++ {
		if candidate == root {
			return true
		}
		candidate = all[candidate].ParentID
	}
	return false
}

// checkDeptInput 校验并规整入参。keepLeader 是部门原来的负责人：新选的负责人必须是启用的用户，原来的停用了也可以保留。
// allUserScopes 报告操作人在用户数据资源的每个权限码上是否都是全部范围（没有的权限码按仅本人算）。
// 挪部门的影响会一直持续，所以范围在超管锁里按已提交的状态算，不用内存里的缓存（D-045）：
// 收窄范围的授权刚提交、内存还没重载时，在途的请求不能再按旧的"全部"挪部门。必须在 WithSuperLock 里调用。
func (s *OrgService) allUserScopes(ctx context.Context, actor auth.Principal) (bool, error) {
	if actor.Super {
		return true, nil
	}
	for _, d := range s.deps.RBAC.DataResources(PortalCode) {
		if d.Code != DataUser {
			continue
		}
		for _, perm := range d.Perms {
			f, err := s.deps.RBAC.DataFilterLocked(ctx, actor, DataUser, perm)
			if err != nil {
				return false, err
			}
			if f.Scope != rbac.ScopeAll {
				return false, nil
			}
		}
	}
	return true, nil
}

func (s *OrgService) checkDeptInput(in *DeptInput) (int, error) {
	name, err := cleanName("name", in.Name)
	if err != nil {
		return 0, err
	}
	in.Name = name
	return checkStatus(in.Status)
}

// checkLeader 校验新选的负责人：必须是启用的用户，并且在操作人"查看用户"的范围内（D-039）——否则部门列表上的
// 负责人名字能把范围外的人一个个试出来；范围外的和不存在的回同一个错误。keepLeader 是部门原来的负责人，停用了也可以保留。
// 明确给出原来的负责人也要在范围内（D-058）：否则"给原负责人成功、给别人失败"，逐个试 ID 就能认出被遮蔽的负责人；
// 看不到他的人要保持原负责人，请求里不给 leaderUserId（调用方不调用这里）。
// 在建部门、改部门的事务里、超管锁内调用（D-045）：负责人行加共享锁，检查之后被并发停用或挪出范围的人不会被存进去。
func (s *OrgService) checkLeader(ctx context.Context, actor auth.Principal, leaderID, keepLeader uint64) error {
	if leaderID == 0 {
		return nil
	}
	bad := httpx.ErrValidation.WithFields(httpx.NewField("leaderUserId", "system.dept.leader", "leader must be an enabled user"))
	u, err := s.users.FindByIDForShare(ctx, leaderID)
	if err != nil || (u.Status != StatusEnabled && leaderID != keepLeader) {
		return bad
	}
	f, err := s.deps.RBAC.DataFilterLocked(ctx, actor, DataUser, PermUserList)
	if err != nil {
		return err
	}
	if !f.Allows(u.DeptID, u.ID) {
		return bad
	}
	return nil
}

func deptConflict(err error) error {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return httpx.NewKey(httpx.CodeConflict, "system.dept.nameTaken", "a department with this name already exists under the same parent").WithCause(err)
	}
	return err
}

// CreateDept 新建部门。
func (s *OrgService) CreateDept(ctx context.Context, actor auth.Principal, in DeptInput) (*DeptView, error) {
	status, err := s.checkDeptInput(&in)
	if err != nil {
		return nil, err
	}
	var leader uint64
	if in.LeaderUserID != nil {
		leader = *in.LeaderUserID
	}
	d := &Dept{
		ParentID: in.ParentID, Name: in.Name, LeaderUserID: leader, Phone: in.Phone, Email: in.Email,
		Status: status, Sort: in.Sort, Remark: in.Remark, CreatedBy: actor.UserID, UpdatedBy: actor.UserID,
	}
	// 先拿超管锁再锁部门表（和改部门、分配角色同一个加锁顺序）：负责人的范围检查要按已提交的授权算（D-045）
	err = s.deps.RBAC.WithActor(ctx, actor, func(ctx context.Context, actor auth.Principal) error {
		all, err := lockDepts(ctx)
		if err != nil {
			return err
		}
		if err := s.checkLeader(ctx, actor, leader, 0); err != nil {
			return err
		}
		if in.ParentID != 0 {
			if _, ok := all[in.ParentID]; !ok {
				return httpx.ErrValidation.WithFields(httpx.NewField("parentId", "system.dept.parent", "parent department does not exist"))
			}
			if depthOf(all, in.ParentID)+1 > maxDeptDepth {
				return httpx.ErrValidation.WithFields(httpx.NewField("parentId", "system.dept.depth", "at most 10 levels", "max", maxDeptDepth))
			}
		}
		now := time.Now().UTC()
		d.CreatedAt, d.UpdatedAt = now, now
		return deptConflict(db.From(ctx).Create(d).Error)
	})
	if err != nil {
		return nil, err
	}
	return s.deptView(ctx, actor, d.ID)
}

// UpdateDept 编辑部门，包括换上级（不能挪到自己或自己的下级下面，挪完不能超过 10 层）。
func (s *OrgService) UpdateDept(ctx context.Context, actor auth.Principal, id uint64, in DeptInput) (*DeptView, error) {
	status, err := s.checkDeptInput(&in)
	if err != nil {
		return nil, err
	}
	// 先拿超管锁再锁部门表：挪部门要求的"全部范围"和负责人的范围检查都按已提交的授权算（D-045）
	err = s.deps.RBAC.WithActor(ctx, actor, func(ctx context.Context, actor auth.Principal) error {
		all, err := lockDepts(ctx)
		if err != nil {
			return err
		}
		cur, ok := all[id]
		if !ok {
			return httpx.ErrNotFound
		}
		leader := cur.LeaderUserID // 没给就保持原来的负责人，不做检查（D-055）
		if in.LeaderUserID != nil {
			leader = *in.LeaderUserID
			if err := s.checkLeader(ctx, actor, leader, cur.LeaderUserID); err != nil {
				return err
			}
		}
		// 挪动部门会改变谁在谁的"下级"里（D-039 第 7 条）：非超管只有在用户资源的每个权限码上都是全部范围时
		// 才能改上级——只看"查看用户"不够：查看是全部、修改是本部门及下级的人，把别的部门挪到自己下面就能改那些人了
		if in.ParentID != cur.ParentID && !actor.Super {
			all, err := s.allUserScopes(ctx, actor)
			if err != nil {
				return err
			}
			if !all {
				return httpx.ErrForbidden.WithFields(httpx.NewField("parentId", "system.dept.moveNeedsAll", "moving a department changes who can see and manage whom; only someone with the 'all' scope on every user permission may do it"))
			}
		}
		if in.ParentID != cur.ParentID && in.ParentID != 0 {
			if _, ok := all[in.ParentID]; !ok {
				return httpx.ErrValidation.WithFields(httpx.NewField("parentId", "system.dept.parent", "parent department does not exist"))
			}
			if isDescendant(all, id, in.ParentID) {
				return httpx.ErrValidation.WithFields(httpx.NewField("parentId", "system.dept.cycle", "cannot move a department under itself or its descendants"))
			}
			if depthOf(all, in.ParentID)+heightOf(all, id) > maxDeptDepth {
				return httpx.ErrValidation.WithFields(httpx.NewField("parentId", "system.dept.depth", "at most 10 levels", "max", maxDeptDepth))
			}
		}
		fields := map[string]any{
			"parent_id": in.ParentID, "name": in.Name, "leader_user_id": leader, "phone": in.Phone, "email": in.Email,
			"status": status, "sort": in.Sort, "remark": in.Remark, "updated_by": actor.UserID, "updated_at": time.Now().UTC(),
		}
		return deptConflict(db.From(ctx).Model(&Dept{}).Where("id = ?", id).Updates(fields).Error)
	})
	if err != nil {
		return nil, err
	}
	return s.deptView(ctx, actor, id)
}

// DeleteDept 删除部门：有下级部门或还有用户时拒绝。
func (s *OrgService) DeleteDept(ctx context.Context, id uint64) error {
	return db.Tx(ctx, func(ctx context.Context) error {
		all, err := lockDepts(ctx)
		if err != nil {
			return err
		}
		if _, ok := all[id]; !ok {
			return httpx.ErrNotFound
		}
		for _, d := range all {
			if d.ParentID == id {
				return httpx.NewKey(httpx.CodeConflict, "system.dept.hasChildren", "the department has sub-departments")
			}
		}
		// 普通读就够：上面已经排他锁住全部部门行，要把人分进这个部门的事务（对部门行加共享锁）要么已经提交、
		// 要么在等我们；事务的快照在拿到锁之后才建立，看得到已提交的分配。不对用户行加锁，避免和编辑用户互相等待
		var users int64
		if err := db.From(ctx).Raw("SELECT COUNT(*) FROM ga_user WHERE dept_id = ?", id).Scan(&users).Error; err != nil {
			return err
		}
		if users > 0 {
			return httpx.NewKey(httpx.CodeConflict, "system.dept.hasUsers", "the department still has users", "n", users)
		}
		return db.From(ctx).Where("id = ?", id).Delete(&Dept{}).Error
	})
}

// DeptOption 是部门下拉选项（含停用的，前端据此置灰）。
type DeptOption struct {
	ID       uint64 `json:"id"`
	ParentID uint64 `json:"parentId"`
	Name     string `json:"name"`
	Status   int    `json:"status"`
}

// DeptOptions 返回全部部门的选项。
func (s *OrgService) DeptOptions(ctx context.Context) ([]DeptOption, error) {
	var out []DeptOption
	err := db.From(ctx).Model(&Dept{}).Select("id, parent_id, name, status").Order("sort ASC, id ASC").Scan(&out).Error
	return out, err
}

// deptSubtree 返回部门自己和全部下级部门的 ID。
func (s *OrgService) deptSubtree(ctx context.Context, root uint64) ([]uint64, error) {
	var rows []DeptOption
	if err := db.From(ctx).Model(&Dept{}).Select("id, parent_id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	children := map[uint64][]uint64{}
	for _, d := range rows {
		children[d.ParentID] = append(children[d.ParentID], d.ID)
	}
	out := []uint64{root}
	for i := 0; i < len(out) && i < len(rows)+1; i++ {
		out = append(out, children[out[i]]...)
	}
	return out, nil
}

// ---- 岗位 ----

// PostView 是岗位的管理视图。
type PostView struct {
	Post
	UserCount int64 `json:"userCount"`
}

// PostInput 是新建、编辑岗位的入参；Code 只在新建时使用。
type PostInput struct {
	Code   string
	Name   string
	Status *int
	Sort   uint
	Remark string
}

// ListPosts 分页列出岗位：keyword 匹配编码和名称。
func (s *OrgService) ListPosts(ctx context.Context, q httpx.PageQuery, status *int) ([]PostView, int64, error) {
	tx := db.From(ctx).Model(&Post{})
	if q.Keyword != "" {
		like := "%" + escapeLike(q.Keyword) + "%"
		tx = tx.Where("code LIKE ? OR name LIKE ?", like, like)
	}
	if status != nil {
		tx = tx.Where("status = ?", *status)
	}
	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []Post
	if err := tx.Order("sort ASC, id ASC").Offset(q.Offset()).Limit(q.Limit()).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	ids := make([]uint64, 0, len(rows))
	for _, p := range rows {
		ids = append(ids, p.ID)
	}
	byPost := map[uint64]int64{}
	if len(ids) > 0 {
		var counts []struct {
			PostID uint64 `gorm:"column:post_id"`
			N      int64  `gorm:"column:n"`
		}
		if err := db.From(ctx).Raw("SELECT post_id, COUNT(*) AS n FROM ga_user_post WHERE post_id IN ? GROUP BY post_id", ids).Scan(&counts).Error; err != nil {
			return nil, 0, err
		}
		for _, c := range counts {
			byPost[c.PostID] = c.N
		}
	}
	out := make([]PostView, 0, len(rows))
	for _, p := range rows {
		out = append(out, PostView{Post: p, UserCount: byPost[p.ID]})
	}
	return out, total, nil
}

func postConflict(err error) error {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return httpx.NewKey(httpx.CodeConflict, "system.post.codeTaken", "post code already exists").WithCause(err)
	}
	return err
}

// CreatePost 新建岗位。
func (s *OrgService) CreatePost(ctx context.Context, actor auth.Principal, in PostInput) (*Post, error) {
	code := strings.TrimSpace(in.Code)
	if !postCodeRe.MatchString(code) {
		return nil, httpx.ErrValidation.WithFields(httpx.NewField("code", "system.post.code", "lowercase letter first, then lowercase letters, digits and hyphens, at most 64"))
	}
	name, err := cleanName("name", in.Name)
	if err != nil {
		return nil, err
	}
	status, err := checkStatus(in.Status)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	p := &Post{Code: code, Name: name, Status: status, Sort: in.Sort, Remark: in.Remark, CreatedAt: now, UpdatedAt: now, CreatedBy: actor.UserID, UpdatedBy: actor.UserID}
	if err := postConflict(db.From(ctx).Create(p).Error); err != nil {
		return nil, err
	}
	return p, nil
}

// UpdatePost 编辑岗位（编码不可改）。
func (s *OrgService) UpdatePost(ctx context.Context, actor auth.Principal, id uint64, in PostInput) (*Post, error) {
	name, err := cleanName("name", in.Name)
	if err != nil {
		return nil, err
	}
	status, err := checkStatus(in.Status)
	if err != nil {
		return nil, err
	}
	var p Post
	err = db.Tx(ctx, func(ctx context.Context) error {
		// 先确认存在再改：同一毫秒重复提交时"改动行数为 0"不代表不存在
		err := db.From(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&p).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := db.From(ctx).Model(&Post{}).Where("id = ?", id).Updates(map[string]any{
			"name": name, "status": status, "sort": in.Sort, "remark": in.Remark, "updated_by": actor.UserID, "updated_at": time.Now().UTC(),
		}).Error; err != nil {
			return err
		}
		return db.From(ctx).Where("id = ?", id).Take(&p).Error
	})
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// DeletePost 删除岗位：还有用户在用时拒绝。
func (s *OrgService) DeletePost(ctx context.Context, id uint64) error {
	return db.Tx(ctx, func(ctx context.Context) error {
		var p Post
		err := db.From(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&p).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return err
		}
		// 同上：岗位行已排他锁住，分配这个岗位的事务要么已提交、要么在等我们
		var n int64
		if err := db.From(ctx).Raw("SELECT COUNT(*) FROM ga_user_post WHERE post_id = ?", id).Scan(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return httpx.NewKey(httpx.CodeConflict, "system.post.inUse", "the post is still assigned to users", "n", n)
		}
		return db.From(ctx).Where("id = ?", id).Delete(&Post{}).Error
	})
}

// PostOption 是岗位下拉选项（含停用的，前端据此置灰）。
type PostOption struct {
	ID     uint64 `json:"id"`
	Code   string `json:"code"`
	Name   string `json:"name"`
	Status int    `json:"status"`
}

// PostOptions 返回全部岗位的选项。
func (s *OrgService) PostOptions(ctx context.Context) ([]PostOption, error) {
	var out []PostOption
	err := db.From(ctx).Model(&Post{}).Select("id, code, name, status").Order("sort ASC, id ASC").Scan(&out).Error
	return out, err
}

// ---- 用户归属 ----

// assignOrg 在调用方的事务里设置用户的部门和岗位（岗位整体替换）。
// 新选的部门、岗位必须存在且启用；原来就有的即使后来停用了也可以保留。部门行、岗位行加共享锁，与删除互斥。
func (s *OrgService) assignOrg(ctx context.Context, u *User, deptID *uint64, postIDs *[]uint64) error {
	if deptID != nil && *deptID != 0 {
		var d Dept
		err := db.From(ctx).Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ?", *deptID).Take(&d).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return httpx.ErrValidation.WithFields(httpx.NewField("deptId", "system.user.dept", "department does not exist"))
		}
		if err != nil {
			return err
		}
		if d.Status != StatusEnabled && d.ID != u.DeptID {
			return httpx.ErrValidation.WithFields(httpx.NewField("deptId", "system.user.deptDisabled", "department is disabled"))
		}
	}
	if deptID != nil && *deptID != u.DeptID {
		if err := db.From(ctx).Model(&User{}).Where("id = ?", u.ID).Update("dept_id", *deptID).Error; err != nil {
			return err
		}
	}
	if postIDs == nil {
		return nil
	}
	ids := dedup(*postIDs)
	if len(ids) > maxUserPosts {
		return httpx.ErrValidation.WithFields(httpx.NewField("postIds", "system.user.posts", "at most 20 posts", "max", maxUserPosts))
	}
	var current []uint64
	if err := db.From(ctx).Model(&userPost{}).Where("user_id = ?", u.ID).Pluck("post_id", &current).Error; err != nil {
		return err
	}
	had := make(map[uint64]bool, len(current))
	for _, id := range current {
		had[id] = true
	}
	if len(ids) > 0 {
		var posts []Post
		if err := db.From(ctx).Clauses(clause.Locking{Strength: "SHARE"}).Where("id IN ?", ids).Find(&posts).Error; err != nil {
			return err
		}
		if len(posts) != len(ids) {
			return httpx.ErrValidation.WithFields(httpx.NewField("postIds", "system.user.postMissing", "post does not exist"))
		}
		for _, p := range posts {
			if p.Status != StatusEnabled && !had[p.ID] {
				return httpx.ErrValidation.WithFields(httpx.NewField("postIds", "system.user.postDisabled", "post is disabled"))
			}
		}
	}
	// 只改差异：没变化时不写，减少并发编辑不同用户时的锁冲突
	want := make(map[uint64]bool, len(ids))
	var add []userPost
	for _, id := range ids {
		want[id] = true
		if !had[id] {
			add = append(add, userPost{UserID: u.ID, PostID: id})
		}
	}
	var drop []uint64
	for _, id := range current {
		if !want[id] {
			drop = append(drop, id)
		}
	}
	if len(drop) > 0 {
		if err := db.From(ctx).Where("user_id = ? AND post_id IN ?", u.ID, drop).Delete(&userPost{}).Error; err != nil {
			return err
		}
	}
	if len(add) > 0 {
		return db.From(ctx).Create(&add).Error
	}
	return nil
}

func dedup(ids []uint64) []uint64 {
	seen := make(map[uint64]bool, len(ids))
	out := make([]uint64, 0, len(ids))
	for _, id := range ids {
		if id != 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// PostRef 是用户视图里的岗位引用。
type PostRef struct {
	ID   uint64 `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

// orgOf 批量取用户的部门名和岗位。
func (s *OrgService) orgOf(ctx context.Context, users []User) (map[uint64]string, map[uint64][]PostRef, error) {
	deptIDs := make([]uint64, 0, len(users))
	userIDs := make([]uint64, 0, len(users))
	for _, u := range users {
		userIDs = append(userIDs, u.ID)
		if u.DeptID != 0 {
			deptIDs = append(deptIDs, u.DeptID)
		}
	}
	names := map[uint64]string{}
	if len(deptIDs) > 0 {
		var ds []Dept
		if err := db.From(ctx).Select("id, name").Where("id IN ?", deptIDs).Find(&ds).Error; err != nil {
			return nil, nil, err
		}
		for _, d := range ds {
			names[d.ID] = d.Name
		}
	}
	posts := map[uint64][]PostRef{}
	if len(userIDs) > 0 {
		var rows []struct {
			UserID uint64 `gorm:"column:user_id"`
			PostRef
		}
		err := db.From(ctx).Raw(`SELECT up.user_id, p.id, p.code, p.name FROM ga_user_post up JOIN ga_post p ON p.id = up.post_id
			WHERE up.user_id IN ? ORDER BY p.sort, p.id`, userIDs).Scan(&rows).Error
		if err != nil {
			return nil, nil, err
		}
		for _, r := range rows {
			posts[r.UserID] = append(posts[r.UserID], r.PostRef)
		}
	}
	return names, posts, nil
}
