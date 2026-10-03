package app

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAuth_193_UnknownRefreshDoesNotLock(t *testing.T) {
	f := newAuthFixture(t)
	lockingReads := 0
	const callback = "test:refresh-lock-observer"
	database := f.app.Deps().DB
	require.NoError(t, database.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if _, ok := tx.Statement.Clauses["FOR"]; ok {
			lockingReads++
		}
	}))
	defer func() { require.NoError(t, database.Callback().Query().Remove(callback)) }()
	r := f.do(http.MethodPost, "/auth/refresh", nil, webClient(), cookieOpt(strings.Repeat("a", 32)+"."+strings.Repeat("b", 64)))
	require.Equal(t, 401, r.rec.Code, r.rec.Body.String())
	require.Equal(t, "auth.sessionEnded", r.env.Key)
	require.Zero(t, lockingReads)
	require.Empty(t, r.rec.Result().Cookies())
}
