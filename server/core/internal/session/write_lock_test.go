package session

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/db"
)

// 规范 §13.2 第 189 条：三个端的后台写事务先取得会话锁时，锁屏等待提交或回滚。
func TestLockWritable_189_SerializesWithLock(t *testing.T) {
	for _, portalCode := range []string{"platform", "agent", "merchant"} {
		for _, rollback := range []bool{false, true} {
			name := portalCode + "/commit"
			if rollback {
				name = portalCode + "/rollback"
			}
			t.Run(name, func(t *testing.T) {
				m, ctx, now := cleanupFixture(t)
				s, _, err := m.Create(ctx, portalCode, 0, 1, "127.0.0.1", "test")
				require.NoError(t, err)
				ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				held, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				finish := func() { once.Do(func() { close(release) }) }
				defer finish()
				done := make(chan error, 1)
				rollbackErr := errors.New("test rollback")
				go func() {
					done <- db.Tx(ctx, func(ctx context.Context) error {
						if err := m.LockWritable(ctx, portalCode, s.SID); err != nil {
							return err
						}
						close(held)
						select {
						case <-release:
						case <-ctx.Done():
							return ctx.Err()
						}
						if rollback {
							return rollbackErr
						}
						return nil
					})
				}()
				select {
				case <-held:
				case err := <-done:
					t.Fatalf("write did not acquire session lock: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				locked := make(chan error, 1)
				go func() { locked <- m.Lock(ctx, portalCode, s.SID) }()
				select {
				case err := <-locked:
					t.Fatalf("lock completed before write transaction ended: %v", err)
				case <-time.After(100 * time.Millisecond):
				}
				finish()
				err = <-done
				if rollback {
					require.ErrorIs(t, err, rollbackErr)
				} else {
					require.NoError(t, err)
				}
				require.NoError(t, <-locked)
				row, err := m.Get(ctx, s.SID)
				require.NoError(t, err)
				require.NotNil(t, row.LockedAt)
				require.True(t, row.Active(now), "locked session remains active")
				require.ErrorIs(t, db.Tx(ctx, func(ctx context.Context) error {
					return m.LockWritable(ctx, portalCode, s.SID)
				}), ErrLocked)
			})
		}
	}
}
