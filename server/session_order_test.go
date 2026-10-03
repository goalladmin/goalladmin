package main

// D-089，规范 §13.2 第 175 条：三个端的实际用户来源、会话和 RBAC 装配，验证事务结束与下线的顺序。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/app"
	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/logx"
	"github.com/goalladmin/goalladmin/server/core/rbac"
	"github.com/goalladmin/goalladmin/server/modules/agentportal"
	"github.com/goalladmin/goalladmin/server/modules/merchantportal"
)

func sessionOrderApp(t *testing.T, f *partnerFixture, code string) (*app.App, func() auth.Principal) {
	t.Helper()
	root := f.admin("root")
	a := f.app
	username, password, orgCode := "root", "changed-pass-9", ""
	var userID, orgID uint64
	require.NoError(t, f.gdb.Table("ga_user").Select("id").Where("username = ?", username).Scan(&userID).Error)
	if code != "platform" {
		o := f.newOrg(root, code, "Session ordering", nil)
		username, password, orgCode, userID, orgID = "admin", o.password, o.code, o.ownerID, o.id
		cfg := conf.Default()
		cfg.Portals = map[string]conf.Portal{code: {AccessTTL: 15 * time.Minute, RefreshTTL: 24 * time.Hour}}
		var err error
		a, err = app.New(cfg, app.WithDB(f.gdb), app.WithoutMigrations(), app.WithPasswordHashParams(64, 1), app.WithLogger(logx.New("error", "text", io.Discard)))
		require.NoError(t, err)
		if code == "agent" {
			a.Register(agentportal.Module())
		} else {
			a.Register(merchantportal.Module())
		}
		require.NoError(t, a.Setup())
	}
	request := func(path, token string, body map[string]string) map[string]any {
		t.Helper()
		b, err := json.Marshal(body)
		require.NoError(t, err)
		method := "POST"
		if path == "/auth/password" {
			method = "PUT"
		}
		r := httptest.NewRequest(method, app.PortalPrefix(code)+path, strings.NewReader(string(b)))
		r.RemoteAddr = "203.0.113.10:5000"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-GA-Client", "web")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		var env httpx.Envelope
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
		require.Zero(t, env.Code, w.Body.String())
		data, _ := env.Data.(map[string]any)
		return data
	}
	login := func() map[string]any {
		return request("/auth/login", "", map[string]string{"org": orgCode, "username": username, "password": password})
	}
	if code != "platform" {
		first := login()
		request("/auth/password", first["accessToken"].(string), map[string]string{"oldPassword": password, "newPassword": "changed-pass-9"})
		password = "changed-pass-9"
	}
	return a, func() auth.Principal {
		data := login()
		return auth.Principal{Portal: code, OrgID: orgID, UserID: userID, Username: username, SessionID: data["sessionId"].(string)}
	}
}

func TestSessionOrder_175_ManagementEndsBeforeRevocation(t *testing.T) {
	for _, code := range []string{"platform", "agent", "merchant"} {
		t.Run(code, func(t *testing.T) {
			f := newPartnerFixture(t)
			a, login := sessionOrderApp(t, f, code)
			for _, mode := range []string{"commit", "rollback"} {
				t.Run(mode, func(t *testing.T) {
					actor, keeper := login(), login()
					ctx, cancel := context.WithTimeout(auth.WithPrincipal(a.Context(context.Background()), actor), 10*time.Second)
					defer cancel()
					ready, release := make(chan struct{}), make(chan struct{})
					unblock := sync.OnceFunc(func() { close(release) })
					defer unblock()
					done := make(chan error, 1)
					rolledBack := errors.New("test transaction rolled back")
					go func() {
						done <- a.Deps().RBAC.WithActor(ctx, actor, func(tx context.Context, current auth.Principal) error {
							_, err := a.Deps().RBAC.CreateRole(tx, current, code, rbac.RoleInput{Code: mode, Name: mode, Status: 1})
							if err != nil {
								return err
							}
							close(ready)
							select {
							case <-release:
							case <-ctx.Done():
								return ctx.Err()
							}
							if mode == "rollback" {
								return rolledBack
							}
							return nil
						})
					}()
					select {
					case <-ready:
					case err := <-done:
						t.Fatalf("management transaction did not reach its write: %v", err)
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
					// 单会话下线验证会话锁；下线其他设备验证账号→会话顺序。两者等待超时均不得写出吊销。
					for _, revoke := range []func(context.Context) error{
						func(c context.Context) error {
							return a.Deps().Auth.RevokeSession(c, code, actor.SessionID, auth.RevokeLogout)
						},
						func(c context.Context) error {
							return a.Deps().RBAC.WithSelf(c, keeper, func(tx context.Context) error {
								_, err := a.Deps().Auth.RevokeOtherSessions(tx, code, keeper.UserID, keeper.SessionID, auth.RevokeLogout)
								return err
							})
						},
					} {
						waiting, stop := context.WithTimeout(a.Context(context.Background()), 150*time.Millisecond)
						err := revoke(waiting)
						require.Error(t, err)
						require.ErrorIs(t, waiting.Err(), context.DeadlineExceeded)
						stop()
					}
					var n int64
					require.NoError(t, f.gdb.Table("ga_session").Where("sid = ? AND revoked_at IS NOT NULL", actor.SessionID).Count(&n).Error)
					require.Zero(t, n, "waiting cancellation must not revoke the session")
					// 保留一个成功的下线调用与事务结束重叠，确认锁释放后可以继续完成。
					revoking, revoked := make(chan struct{}), make(chan error, 1)
					go func() {
						close(revoking)
						revoked <- a.Deps().RBAC.WithSelf(ctx, keeper, func(tx context.Context) error {
							_, err := a.Deps().Auth.RevokeOtherSessions(tx, code, keeper.UserID, keeper.SessionID, auth.RevokeLogout)
							return err
						})
					}()
					<-revoking
					unblock()
					if mode == "rollback" {
						require.ErrorIs(t, <-done, rolledBack)
					} else {
						require.NoError(t, <-done)
					}
					require.NoError(t, <-revoked)
					require.NoError(t, f.gdb.Table("ga_role").Where("portal = ? AND code = ?", code, mode).Count(&n).Error)
					if mode == "rollback" {
						require.Zero(t, n)
					} else {
						require.EqualValues(t, 1, n)
						role, err := a.Deps().RBAC.RoleByCode(ctx, code, mode)
						require.NoError(t, err)
						_, err = a.Deps().RBAC.UpdateRole(ctx, actor, code, role.ID, rbac.RoleInput{Name: "changed", Status: 1})
						require.ErrorIs(t, err, httpx.ErrTokenInvalid)
					}
					// 直接调用角色服务也必须重新认定，不能只保护显式 WithActor 的调用方。
					_, err := a.Deps().RBAC.CreateRole(ctx, actor, code, rbac.RoleInput{Code: "after", Name: "after", Status: 1})
					require.ErrorIs(t, err, httpx.ErrTokenInvalid)
					require.NoError(t, a.Deps().RBAC.WithActor(ctx, keeper, func(context.Context, auth.Principal) error { return nil }))
				})
			}
		})
	}
}

func TestSessionOrder_175_RevocationOverridesEarlierSnapshot(t *testing.T) {
	f := newPartnerFixture(t)
	a, login := sessionOrderApp(t, f, "platform")
	actor := login()
	ctx := auth.WithPrincipal(a.Context(context.Background()), actor)
	// 平台允许在外层事务中调用；已有普通读快照也不能覆盖后来的会话锁定读结果。
	err := db.Tx(ctx, func(tx context.Context) error {
		var n int64
		if err := db.From(tx).Table("ga_session").Where("sid = ? AND revoked_at IS NULL", actor.SessionID).Count(&n).Error; err != nil {
			return err
		}
		require.EqualValues(t, 1, n)
		if err := a.Deps().Auth.RevokeSession(ctx, "platform", actor.SessionID, auth.RevokeLogout); err != nil {
			return err
		}
		_, err := a.Deps().RBAC.CreateRole(tx, actor, "platform", rbac.RoleInput{Code: "after", Name: "after", Status: 1})
		return err
	})
	require.ErrorIs(t, err, httpx.ErrTokenInvalid)
	var n int64
	require.NoError(t, f.gdb.Table("ga_role").Where("portal = ? AND code = ?", "platform", "after").Count(&n).Error)
	require.Zero(t, n)
}
