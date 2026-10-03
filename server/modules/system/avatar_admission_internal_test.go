package system

import (
	"context"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/stretchr/testify/require"
)

type untouchedAvatarBody struct{ reads int }

func (b *untouchedAvatarBody) Read([]byte) (int, error) { b.reads++; return 0, io.EOF }
func (*untouchedAvatarBody) Close() error               { return nil }

func TestAvatar_197_AdmissionBeforeRead(t *testing.T) {
	s := NewUserService(nil, nil, nil)
	ctx, release, err := s.admitAvatar(context.Background(), 8)
	require.NoError(t, err)
	_, nestedRelease, err := s.admitAvatar(ctx, 8)
	require.NoError(t, err)
	nestedRelease()
	_, _, err = s.admitAvatar(context.Background(), 8)
	require.ErrorIs(t, err, httpx.ErrTooManyRequests)
	_, release2, err := s.admitAvatar(context.Background(), 9)
	require.NoError(t, err)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := &handlers{users: s}
	r.POST("/avatar", func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), auth.Principal{Portal: "platform", UserID: 10}))
		c.Next()
	}, h.admitAvatar, func(c *gin.Context) { _, _ = io.ReadAll(c.Request.Body); t.Fatal("满位不执行预读") })
	body := &untouchedAvatarBody{}
	req := httptest.NewRequest("POST", "/avatar", body)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, 429, w.Code)
	require.Zero(t, body.reads)
	release2()
	release()
	_, done, err := s.admitAvatar(context.Background(), 10)
	require.NoError(t, err)
	done()
}
