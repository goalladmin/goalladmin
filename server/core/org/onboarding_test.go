package org_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/org"
	"github.com/stretchr/testify/require"
)

func applicationInput() org.ApplicationInput {
	return org.ApplicationInput{Name: "New merchant", ContactName: "Contact", ContactPhone: "12345678", OwnerUsername: "owner"}
}
func reviewer() auth.Principal {
	return auth.Principal{Portal: "platform", UserID: platformAdmin, Super: true}
}

func TestOnboarding_169_ReviewOnceAndRollback(t *testing.T) {
	f := setup(t)
	a, err := f.svc.Apply(f.ctx, org.Merchant(), applicationInput())
	require.NoError(t, err)
	var n int64
	require.NoError(t, f.gdb.Table("ga_merchant").Count(&n).Error)
	require.Zero(t, n)
	require.NoError(t, f.gdb.Table("ga_merchant_user").Count(&n).Error)
	require.Zero(t, n)
	pwd := f.pwd()
	rollback := errors.New("rollback")
	err = db.Tx(f.ctx, func(ctx context.Context) error {
		_, e := f.svc.ReviewApplication(ctx, reviewer(), a.ID, true, "", pwd)
		require.NoError(t, e)
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	require.NoError(t, f.gdb.Table("ga_merchant").Count(&n).Error)
	require.Zero(t, n)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, e := f.svc.ReviewApplication(f.ctx, reviewer(), a.ID, true, "", pwd); results <- e })
	}
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
		}
	}
	require.Equal(t, 1, success)
	require.NoError(t, f.gdb.Table("ga_merchant").Count(&n).Error)
	require.EqualValues(t, 1, n)
	var must bool
	require.NoError(t, f.gdb.Table("ga_merchant_user").Select("must_change_pwd").Scan(&must).Error)
	require.True(t, must)
	_, err = f.svc.ReviewApplication(f.ctx, reviewer(), a.ID, false, "reject", org.InitialPassword{})
	require.Error(t, err)
	rows, n, err := f.svc.Applications(f.ctx, "approved", httpx.PageQuery{Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	require.NotEmpty(t, rows[0].OrgCode)
	require.Equal(t, "approved", rows[0].ReviewState)
	b, err := json.Marshal(rows)
	require.NoError(t, err)
	require.NotContains(t, string(b), "password")
}

func TestOnboarding_169_InvitationLifecycle(t *testing.T) {
	f := setup(t)
	agent := f.create(org.Agent(), "Agent", "owner", 0)
	actor := auth.Principal{Portal: "agent", OrgID: agent.Org.ID, UserID: agent.OwnerID, Super: true}
	invite, token, err := f.svc.CreateInvitation(f.ctx, actor)
	require.NoError(t, err)
	require.Len(t, token, 64)
	raw, err := json.Marshal(invite)
	require.NoError(t, err)
	require.NotContains(t, string(raw), token)
	require.NotContains(t, string(raw), "token")
	in := applicationInput()
	in.InvitationToken = token
	_, err = f.svc.Apply(f.ctx, org.Agent(), in)
	require.Error(t, err)
	// 外层事务失败不能消耗邀请。
	rollback := errors.New("rollback")
	err = db.Tx(f.ctx, func(ctx context.Context) error {
		_, e := f.svc.Apply(ctx, org.Merchant(), in)
		require.NoError(t, e)
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	a, err := f.svc.Apply(f.ctx, org.Merchant(), in)
	require.NoError(t, err)
	require.Equal(t, actor.OrgID, a.AgentID)
	_, err = f.svc.Apply(f.ctx, org.Merchant(), in)
	require.Error(t, err)
	require.NoError(t, f.svc.SetStatus(f.ctx, org.Agent(), actor.OrgID, 0, platformAdmin))
	_, err = f.svc.ReviewApplication(f.ctx, reviewer(), a.ID, true, "", f.pwd())
	require.Error(t, err)
	require.NoError(t, f.svc.SetStatus(f.ctx, org.Agent(), actor.OrgID, 1, platformAdmin))
	result, err := f.svc.ReviewApplication(f.ctx, reviewer(), a.ID, true, "checked", f.pwd())
	require.NoError(t, err)
	require.Equal(t, actor.OrgID, result.Org.AgentID)
	require.NotEmpty(t, result.Password)
	for _, mode := range []string{"expired", "revoked"} {
		v, tok, e := f.svc.CreateInvitation(f.ctx, actor)
		require.NoError(t, e)
		if mode == "expired" {
			require.NoError(t, f.gdb.Table("ga_org_invitation").Where("id = ?", v.ID).Update("expires_at", f.now.Add(-time.Second)).Error)
		} else {
			require.NoError(t, f.svc.RevokeInvitation(f.ctx, actor, v.ID))
		}
		in.InvitationToken = tok
		_, e = f.svc.Apply(f.ctx, org.Merchant(), in)
		require.Error(t, e)
	}
	other := actor
	other.OrgID++
	require.ErrorIs(t, f.svc.RevokeInvitation(f.ctx, other, invite.ID), httpx.ErrNotFound)
	rows, n, err := f.svc.Invitations(f.ctx, other, httpx.PageQuery{Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.Zero(t, n)
	require.Empty(t, rows)
	actor.Super = false
	_, _, err = f.svc.CreateInvitation(f.ctx, actor)
	require.ErrorIs(t, err, httpx.ErrForbidden)
}

func TestOnboarding_169_RejectAndValidation(t *testing.T) {
	f := setup(t)
	in := applicationInput()
	in.ContactPhone = ""
	_, err := f.svc.Apply(f.ctx, org.Merchant(), in)
	require.Error(t, err)
	a, err := f.svc.Apply(f.ctx, org.Agent(), applicationInput())
	require.NoError(t, err)
	_, err = f.svc.ReviewApplication(f.ctx, reviewer(), a.ID, false, "", org.InitialPassword{})
	require.Error(t, err)
	_, err = f.svc.ReviewApplication(f.ctx, auth.Principal{Portal: "agent", UserID: 7}, a.ID, false, "no", org.InitialPassword{})
	require.ErrorIs(t, err, httpx.ErrForbidden)
	_, err = f.svc.ReviewApplication(f.ctx, reviewer(), a.ID, false, "contact not verified", org.InitialPassword{})
	require.NoError(t, err)
	var n int64
	require.NoError(t, f.gdb.Table("ga_agent").Count(&n).Error)
	require.Zero(t, n)
	_, err = f.svc.ReviewApplication(f.ctx, reviewer(), a.ID, true, "", f.pwd())
	require.Error(t, err)
}

func TestOnboarding_169_ConcurrentInvitationUse(t *testing.T) {
	f := setup(t)
	a := f.create(org.Agent(), "Inviter", "owner", 0)
	_, token, err := f.svc.CreateInvitation(f.ctx, auth.Principal{Portal: "agent", OrgID: a.Org.ID, UserID: a.OwnerID, Super: true})
	require.NoError(t, err)
	in := applicationInput()
	in.InvitationToken = token
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { _, err := f.svc.Apply(f.ctx, org.Merchant(), in); results <- err })
	}
	wg.Wait()
	close(results)
	ok := 0
	for err := range results {
		if err == nil {
			ok++
		}
	}
	require.Equal(t, 1, ok)
	var n int64
	require.NoError(t, f.gdb.Table("ga_org_application").Count(&n).Error)
	require.EqualValues(t, 1, n)
}
