package system_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/modules/system"
)

type accountInvalidation struct {
	portal string
	userID uint64
	name   string // 回调执行时，另一条连接读到的已提交显示名
}

type accountInvalidationProbe struct {
	auth.Service
	mu    sync.Mutex
	calls []accountInvalidation
	read  func(uint64) string
}

func (p *accountInvalidationProbe) ForgetAccount(portal string, userID uint64) {
	call := accountInvalidation{portal: portal, userID: userID, name: p.read(userID)}
	p.mu.Lock()
	p.calls = append(p.calls, call)
	p.mu.Unlock()
	p.Service.ForgetAccount(portal, userID)
}

func (p *accountInvalidationProbe) snapshot() []accountInvalidation {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]accountInvalidation(nil), p.calls...)
}

// D-075：服务加入外层事务时，资料更新只有成功提交才清账号缓存并通知；回滚不影响缓存。
func TestUserInvalidation_164_OuterTransactionCommitAndRollback(t *testing.T) {
	for _, operation := range []string{"update", "profile"} {
		t.Run(operation, func(t *testing.T) {
			f := newFixture(t)
			rootToken, rootID := f.admin("root")
			userID, userToken := f.createUser(rootToken, "alice", nil)
			deps := f.app.Deps()
			ctx := f.app.Context(context.Background())
			readName := func(id uint64) string {
				var name string
				require.NoError(t, f.gdb.Raw("SELECT display_name FROM ga_user WHERE id = ?", id).Scan(&name).Error)
				return name
			}
			probe := &accountInvalidationProbe{Service: deps.Auth, read: readName}
			deps.Auth = probe
			repo := system.NewUserRepo()
			svc := system.NewUserService(deps, repo, system.NewOrgService(deps, repo))
			principal := func(id uint64, username string, super bool) auth.Principal {
				var sid string
				require.NoError(t, f.gdb.Raw("SELECT sid FROM ga_session WHERE portal = 'platform' AND user_id = ? AND revoked_at IS NULL", id).Scan(&sid).Error)
				require.NotEmpty(t, sid)
				return auth.Principal{Portal: "platform", UserID: id, SessionID: sid, Username: username, Super: super}
			}
			actor := principal(rootID, "root", true)
			self := principal(userID, "alice", false)
			meName := func() string {
				r := f.do(userToken, "GET", "/auth/me", nil)
				require.Zero(t, r.env.Code, r.rec.Body.String())
				return r.data()["user"].(map[string]any)["displayName"].(string)
			}
			original := readName(userID)
			require.Equal(t, original, meName(), "先填入账号状态缓存")
			rollback := errors.New("rollback")
			for _, committed := range []bool{false, true} {
				err := db.Tx(ctx, func(txctx context.Context) error {
					if operation == "update" {
						view, err := svc.Update(txctx, actor, userID, system.UpdateInput{DisplayName: "Alice Updated"})
						require.NoError(t, err)
						require.Equal(t, "Alice Updated", view.DisplayName)
					} else {
						view, err := svc.UpdateProfile(txctx, self, system.ProfileInput{DisplayName: "Alice Updated"})
						require.NoError(t, err)
						require.Equal(t, "Alice Updated", view.DisplayName)
					}
					require.Empty(t, probe.snapshot(), "未提交不能清缓存或通知")
					require.Equal(t, original, readName(userID), "另一条连接仍只能看到提交前的资料")
					require.Equal(t, original, meName(), "外层提交前账号缓存仍有效")
					if !committed {
						return rollback
					}
					return nil
				})
				if committed {
					require.NoError(t, err)
					require.Equal(t, []accountInvalidation{{portal: "platform", userID: userID, name: "Alice Updated"}}, probe.snapshot(), "提交后恰好通知一次，回调读到已提交的资料")
					require.Equal(t, "Alice Updated", readName(userID))
					require.Equal(t, "Alice Updated", meName(), "提交后的下一请求读到新资料")
				} else {
					require.ErrorIs(t, err, rollback)
					require.Empty(t, probe.snapshot(), "回滚不通知")
					require.Equal(t, original, readName(userID))
					require.Equal(t, original, meName())
				}
			}
		})
	}
}
