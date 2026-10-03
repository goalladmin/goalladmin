package system

import (
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/httpx"
)

// 个人中心（D-038）：三条接口都只针对调用者本人，身份只从 Principal 取，路径和请求体里没有用户 ID。

// profile 处理 GET /system/profile。
func (h *handlers) profile(c *gin.Context) {
	ctx := c.Request.Context()
	v, err := h.users.Profile(ctx, auth.MustFromCtx(ctx))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, v)
}

// updateProfileRequest 是本人能改的字段。严格解析：夹带用户名、状态、角色等别的字段直接回 3002。
type updateProfileRequest struct {
	DisplayName string `json:"displayName" binding:"required,max=64"`
	Email       string `json:"email" binding:"omitempty,email,max=128"`
	Phone       string `json:"phone" binding:"max=32"`
	Bio         string `json:"bio" binding:"max=255"`
}

// updateProfile 处理 PUT /system/profile。
func (h *handlers) updateProfile(c *gin.Context) {
	var req updateProfileRequest
	if err := httpx.BindJSONStrict(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	ctx := c.Request.Context()
	v, err := h.users.UpdateProfile(ctx, auth.MustFromCtx(ctx), ProfileInput(req))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, v)
}

// revokeOtherSessions 处理 POST /system/profile/revoke-other-sessions：本人除当前会话外的会话全部下线。
func (h *handlers) revokeOtherSessions(c *gin.Context) {
	ctx := c.Request.Context()
	n, err := h.users.RevokeOtherSessions(ctx, auth.MustFromCtx(ctx))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"revoked": n})
}

// ---- 头像（D-040）----

// requireImageBody 只放行 Content-Type 是 image/jpeg 或 image/png 的上传（排在操作日志之前）：
// 别的类型（例如表单编码）会让操作日志去解析请求体，把图片里的内容（EXIF 等）记下来。
func requireImageBody(c *gin.Context) {
	ct, _, _ := mime.ParseMediaType(c.ContentType())
	if ct != "image/jpeg" && ct != "image/png" {
		httpx.Fail(c, avatarError("system.avatar.type", "send the image itself with Content-Type image/jpeg or image/png"))
		c.Abort()
		return
	}
	c.Next()
}

// admitAvatar 排在操作记录预读之前，满位时不读取图片请求体。
func (h *handlers) admitAvatar(c *gin.Context) {
	ctx, release, err := h.users.admitAvatar(c.Request.Context(), auth.MustFromCtx(c.Request.Context()).UserID)
	if err != nil {
		httpx.Fail(c, err)
		c.Abort()
		return
	}
	defer release()
	c.Request = c.Request.WithContext(ctx)
	c.Next()
}

// uploadAvatar 处理 POST /system/avatar：请求体就是图片本身（JPEG 或 PNG）。
func (h *handlers) uploadAvatar(c *gin.Context) {
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			httpx.Fail(c, httpx.ErrBodyTooLarge.WithCause(err))
			return
		}
		httpx.Fail(c, httpx.ErrValidation.WithCause(err))
		return
	}
	ctx := c.Request.Context()
	v, err := h.users.UploadAvatar(ctx, auth.MustFromCtx(ctx), raw)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"avatar": v})
}

type presetAvatarRequest struct {
	Preset string `json:"preset" binding:"required,max=32"`
}

// setPresetAvatar 处理 PUT /system/avatar：选一个内置头像。
func (h *handlers) setPresetAvatar(c *gin.Context) {
	var req presetAvatarRequest
	if err := httpx.BindJSONStrict(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	ctx := c.Request.Context()
	v, err := h.users.SetPresetAvatar(ctx, auth.MustFromCtx(ctx), req.Preset)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"avatar": v})
}

// clearOwnAvatar 处理 DELETE /system/avatar。
func (h *handlers) clearOwnAvatar(c *gin.Context) {
	ctx := c.Request.Context()
	if err := h.users.ClearOwnAvatar(ctx, auth.MustFromCtx(ctx)); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"avatar": ""})
}

// avatarImage 处理 GET /system/avatars/:key：返回 {image: "data:image/jpeg;base64,..."}，size=64 时是小图。
func (h *handlers) avatarImage(c *gin.Context) {
	img, err := h.users.AvatarImage(c.Request.Context(), c.Param("key"), c.Query("size") == "64")
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	// 是别人的照片：不进浏览器的磁盘缓存（前端在内存里按键缓存，重复显示不会重复请求）
	c.Header("Cache-Control", "no-store")
	httpx.OK(c, gin.H{"image": img})
}

// clearUserAvatar 处理 DELETE /system/users/:id/avatar：管理员只能清除别人的头像。
func (h *handlers) clearUserAvatar(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if err := h.users.ClearUserAvatar(ctx, auth.MustFromCtx(ctx), id); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nil)
}
