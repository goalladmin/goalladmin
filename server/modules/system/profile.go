package system

import (
	"context"
	"strings"
	"time"
	"unicode"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/httpx"
)

// 个人中心（docs/decisions.md D-038）：本人看本人、本人改本人。
// 这里的方法都只按 Principal 里的用户 ID 读写，不接受任何指定用户的参数；能改别人的仍然只有管理员（UserService.Update）。

// ProfileView 是个人中心的本人视图。只给本人看，所以没有管理员的备注、排序和状态。
type ProfileView struct {
	ID           uint64     `json:"id"`
	Username     string     `json:"username"`
	DisplayName  string     `json:"displayName"`
	Email        string     `json:"email"`
	Phone        string     `json:"phone"`
	Avatar       string     `json:"avatar"`
	Bio          string     `json:"bio"`
	Super        bool       `json:"super"`
	Roles        []RoleRef  `json:"roles"`
	DeptName     string     `json:"deptName"` // 没有部门或部门已不存在时为空
	Posts        []PostRef  `json:"posts"`
	PwdChangedAt *time.Time `json:"pwdChangedAt"` // 上次改密时间，为空表示从未改过
	LastLoginAt  *time.Time `json:"lastLoginAt"`
	LastLoginIP  string     `json:"lastLoginIp"`
	CreatedAt    time.Time  `json:"createdAt"`
	Sessions     int64      `json:"sessions"` // 当前有效的会话数，含本次
}

// ProfileInput 是本人能改的几项。用户名、状态、角色、部门、岗位、头像、排序、备注都不在这里（D-038 第 2 条）。
type ProfileInput struct {
	DisplayName string
	Email       string
	Phone       string
	Bio         string
}

// 个人简介的长度上限（字符数，与 ga_user.bio 的列宽一致）。
const bioMaxChars = 255

// Profile 返回调用者本人的资料。
func (s *UserService) Profile(ctx context.Context, p auth.Principal) (*ProfileView, error) {
	u, err := s.repo.FindByID(ctx, p.UserID)
	if err != nil {
		return nil, notFound(err)
	}
	roles, err := s.deps.RBAC.UserRoles(ctx, p.Portal, u.ID)
	if err != nil {
		return nil, err
	}
	deptNames, posts, err := s.org.orgOf(ctx, []User{*u})
	if err != nil {
		return nil, err
	}
	_, sessions, err := s.deps.Auth.ListSessions(ctx, p.Portal, u.ID, 1, 1)
	if err != nil {
		return nil, err
	}
	refs := make([]RoleRef, 0, len(roles))
	for _, r := range roles {
		refs = append(refs, RoleRef{ID: r.ID, Code: r.Code, Name: r.Name, IsSuper: r.IsSuper})
	}
	ps := posts[u.ID]
	if ps == nil {
		ps = []PostRef{}
	}
	return &ProfileView{
		ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Email: u.Email, Phone: u.Phone, Avatar: u.Avatar, Bio: u.Bio,
		Super: p.Super, Roles: refs, DeptName: deptNames[u.DeptID], Posts: ps,
		PwdChangedAt: u.PwdChangedAt, LastLoginAt: u.LastLoginAt, LastLoginIP: u.LastLoginIP, CreatedAt: u.CreatedAt,
		Sessions: sessions,
	}, nil
}

// UpdateProfile 修改调用者本人的显示名、邮箱、手机和个人简介。
// 显示名与部门名同一套字符校验；简介允许换行，但不能有别的控制字符和不可见字符。
func (s *UserService) UpdateProfile(ctx context.Context, p auth.Principal, in ProfileInput) (*ProfileView, error) {
	name, err := cleanName("displayName", in.DisplayName)
	if err != nil {
		return nil, err
	}
	bio, err := cleanBio(in.Bio)
	if err != nil {
		return nil, err
	}
	// 本人的写操作同样在锁里重新认定（D-048）：请求途中账号被停用、会话被吊销的，这次修改作废（401）。
	// 只锁本人的账号行，不拿全端的超管锁（D-058）
	err = s.deps.RBAC.WithSelf(ctx, p, func(ctx context.Context) error {
		if err := s.repo.Update(ctx, p.UserID, map[string]any{
			"display_name": name, "email": strings.TrimSpace(in.Email), "phone": strings.TrimSpace(in.Phone), "bio": bio,
			"updated_by": p.UserID,
		}); err != nil {
			return notFound(err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// 显示名进了账号状态缓存，清掉让 /auth/me 立即看到新值
	s.deps.Auth.ForgetAccount(p.Portal, p.UserID)
	return s.Profile(ctx, p)
}

// cleanBio 去掉首尾空白，允许换行和制表，拒绝别的控制字符和不可见的格式字符，最多 bioMaxChars 个字符。
func cleanBio(s string) (string, error) {
	s = strings.TrimSpace(s)
	n := 0
	for _, r := range s {
		n++
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return "", httpx.ErrValidation.WithFields(httpx.NewField("bio", "system.org.nameChars", "must not contain control or invisible characters"))
		}
	}
	if n > bioMaxChars {
		return "", httpx.ErrValidation.WithFields(httpx.NewField("bio", "common.maxLength", "must be at most 255 characters", "max", bioMaxChars))
	}
	return s, nil
}

// RevokeOtherSessions 让调用者本人除当前会话外的全部会话下线（个人中心"下线其他设备"）。返回下线的会话数。
// 只按本人的身份（端、用户 ID、当前会话 ID）一条语句吊销，不逐个按会话 ID 处理：会话再多也没有边界问题（D-043）。
func (s *UserService) RevokeOtherSessions(ctx context.Context, p auth.Principal) (int64, error) {
	var n int64
	err := s.deps.RBAC.WithSelf(ctx, p, func(ctx context.Context) error {
		var err error
		n, err = s.deps.Auth.RevokeOtherSessions(ctx, p.Portal, p.UserID, p.SessionID, auth.RevokeLogout)
		return err
	})
	return n, err
}
