package system

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/goalladmin/goalladmin/server/core/auth"
)

// withActor 拿超管锁、按库里的当前状态重新认定操作人，然后执行 fn（D-047）。
// 操作人在请求途中被停用、会话被吊销时返回 401，fn 不会执行。
func withActor[T any](h *handlers, c *gin.Context, fn func(ctx context.Context, actor auth.Principal) (T, error)) (T, error) {
	ctx := c.Request.Context()
	var out T
	err := h.deps.RBAC.WithActor(ctx, auth.MustFromCtx(ctx), func(ctx context.Context, actor auth.Principal) error {
		var err error
		out, err = fn(ctx, actor)
		return err
	})
	return out, err
}
