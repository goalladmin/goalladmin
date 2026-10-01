package system

// 头像（docs/decisions.md D-040）：本人上传或选内置头像；管理员只能清除。
//
// ga_user.avatar 只会是三种值之一：空（显示名字首字母）、preset:<名字>（壳里自带的内置头像）、
// upload:<键>（本人上传，图在 ga_user_avatar）。读上传的头像要登录、按随机键读，换头像或清除后旧键立刻失效。

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"regexp"
	"slices"
	"time"

	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
)

// avatarPresets 是内置头像的名字，和壳里的 AVATAR_PRESETS（web/packages/shell/src/avatar.ts）是同一份表，由测试对齐。
var avatarPresets = []string{
	"aurora", "ocean", "forest", "sunset", "berry", "slate",
	"sand", "mint", "coral", "violet", "sky", "amber",
}

// ga_user.avatar 的前缀。
const (
	avatarPresetPrefix = "preset:"
	avatarUploadPrefix = "upload:"
)

var avatarKeyRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// userAvatar 对应 ga_user_avatar。
type userAvatar struct {
	UserID    uint64    `gorm:"column:user_id;primaryKey"`
	AvatarKey string    `gorm:"column:avatar_key"`
	Image     []byte    `gorm:"column:image"`
	Thumb     []byte    `gorm:"column:thumb"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

func (userAvatar) TableName() string { return "ga_user_avatar" }

// setAvatar 在一个事务里换掉用户的头像：删掉原来上传的图（旧键随之失效），需要时写入新图，再改 ga_user.avatar。
func (s *UserService) setAvatar(ctx context.Context, userID uint64, value string, row *userAvatar, updatedBy uint64) error {
	return db.Tx(ctx, func(ctx context.Context) error {
		// 先锁用户行：同一个人的并发修改排队，加锁顺序和管理员清除一致（先 ga_user、再 ga_user_avatar），不会互相死锁
		if _, err := s.repo.FindByIDForUpdate(ctx, userID); err != nil {
			return notFound(err)
		}
		if err := db.From(ctx).Where("user_id = ?", userID).Delete(&userAvatar{}).Error; err != nil {
			return err
		}
		if row != nil {
			if err := db.From(ctx).Create(row).Error; err != nil {
				return err
			}
		}
		if err := s.repo.Update(ctx, userID, map[string]any{"avatar": value, "updated_by": updatedBy}); err != nil {
			return notFound(err)
		}
		// 头像进了账号状态缓存（/auth/me 返回它）：提交之后清掉
		db.AfterCommit(ctx, func() { s.deps.Auth.ForgetAccount(PortalCode, userID) })
		return nil
	})
}

func newAvatarKey() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// UploadAvatar 本人上传头像：服务器重新生成图片后存下，返回新的 avatar 值。
func (s *UserService) UploadAvatar(ctx context.Context, p auth.Principal, raw []byte) (string, error) {
	// 解码和缩放要内存和 CPU：整个进程同时最多处理 avatarSlots 个，其余的排队，等到请求时限就放弃
	select {
	case s.avatarSlots <- struct{}{}:
		defer func() { <-s.avatarSlots }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	big, thumb, err := processAvatar(raw)
	if err != nil {
		return "", err
	}
	key, err := newAvatarKey()
	if err != nil {
		return "", err
	}
	value := avatarUploadPrefix + key
	row := &userAvatar{UserID: p.UserID, AvatarKey: key, Image: big, Thumb: thumb, CreatedAt: time.Now().UTC()}
	if err := s.setOwnAvatar(ctx, p, value, row); err != nil {
		return "", err
	}
	return value, nil
}

// setOwnAvatar 在锁里重新认定本人之后改头像（D-048）：请求途中账号被停用、会话被吊销的，这次修改作废（401）。
// 只锁本人的账号行，不拿全端的超管锁（D-058）。
func (s *UserService) setOwnAvatar(ctx context.Context, p auth.Principal, value string, row *userAvatar) error {
	return s.deps.RBAC.WithSelf(ctx, p, func(ctx context.Context) error {
		return s.setAvatar(ctx, p.UserID, value, row, p.UserID)
	})
}

// SetPresetAvatar 本人选一个内置头像。
func (s *UserService) SetPresetAvatar(ctx context.Context, p auth.Principal, name string) (string, error) {
	if !slices.Contains(avatarPresets, name) {
		return "", httpx.ErrValidation.WithFields(httpx.NewField("preset", "system.avatar.preset", "unknown built-in avatar"))
	}
	value := avatarPresetPrefix + name
	if err := s.setOwnAvatar(ctx, p, value, nil); err != nil {
		return "", err
	}
	return value, nil
}

// ClearOwnAvatar 本人清除头像（回到名字首字母）。
func (s *UserService) ClearOwnAvatar(ctx context.Context, p auth.Principal) error {
	return s.setOwnAvatar(ctx, p, "", nil)
}

// ClearUserAvatar 管理员清除别人的头像：要在"修改用户"的数据范围内（D-039），非超管不能动超管账号（D-035）。
func (s *UserService) ClearUserAvatar(ctx context.Context, actor auth.Principal, id uint64) error {
	return s.deps.RBAC.WithActor(ctx, actor, func(ctx context.Context, actor auth.Principal) error {
		u, err := s.repo.FindByIDForUpdate(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if err := s.checkWriteTarget(ctx, actor, PermUserUpdate, u); err != nil {
			return err
		}
		if err := s.guardSuperTarget(ctx, actor, id); err != nil {
			return err
		}
		return s.setAvatar(ctx, id, "", nil, actor.UserID)
	})
}

// AvatarImage 按键读上传的头像，返回 data: 地址；thumb 为 true 时返回 64×64 的小图。键不对或已失效时 404。
func (s *UserService) AvatarImage(ctx context.Context, key string, thumb bool) (string, error) {
	if !avatarKeyRe.MatchString(key) {
		return "", httpx.ErrNotFound
	}
	var row userAvatar
	err := db.From(ctx).Where("avatar_key = ?", key).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", httpx.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	img := row.Image
	if thumb {
		img = row.Thumb
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(img), nil
}

// AvatarPresets 返回内置头像的名字（副本）。
func AvatarPresets() []string { return slices.Clone(avatarPresets) }
