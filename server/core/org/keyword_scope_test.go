package org_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/org"
)

// 同一关键字命中其他主体时，列表和总数仍同时受主体、状态条件约束。
func TestAccountsKeywordKeepsOrgAndStatus(t *testing.T) {
	for _, kind := range []org.Kind{org.Agent(), org.Merchant()} {
		t.Run(kind.Portal(), func(t *testing.T) {
			f := setup(t)
			current := f.create(kind, "当前主体", "admin", 0)
			other := f.create(kind, "其他主体", "admin", 0)
			byUsername := f.addAccount(kind, current.Org.ID, "match-name", org.StatusEnabled)
			byDisplay := f.addAccount(kind, current.Org.ID, "staff-local", org.StatusEnabled)
			disabled := f.addAccount(kind, current.Org.ID, "off-local", org.StatusDisabled)
			f.addAccount(kind, other.Org.ID, "match-name", org.StatusEnabled)
			foreign := f.addAccount(kind, other.Org.ID, "staff-foreign", org.StatusEnabled)
			private := f.addAccount(kind, other.Org.ID, "staff-private", org.StatusEnabled)
			table := map[string]string{"agent": "ga_agent_user", "merchant": "ga_merchant_user"}[kind.Portal()]
			require.NoError(t, f.gdb.Table(table).Where("id IN ?", []uint64{byDisplay, disabled, foreign}).Update("display_name", "match-title").Error)
			require.NoError(t, f.gdb.Table(table).Where("id = ?", private).Update("display_name", "private-title").Error)
			enabled := org.StatusEnabled
			for _, tc := range []struct {
				keyword string
				want    []uint64
			}{{"match-name", []uint64{byUsername}}, {"match-title", []uint64{byDisplay}}, {"private-title", nil}} {
				t.Run(tc.keyword, func(t *testing.T) {
					rows, total, err := f.svc.Accounts(f.ctx, kind, current.Org.ID, &enabled, httpx.PageQuery{Page: 1, PageSize: 20, Keyword: tc.keyword})
					require.NoError(t, err)
					require.EqualValues(t, len(tc.want), total)
					ids := make([]uint64, 0, len(rows))
					for _, row := range rows {
						require.Equal(t, current.Org.ID, row.OrgID)
						require.Equal(t, org.StatusEnabled, row.Status)
						ids = append(ids, row.ID)
					}
					require.ElementsMatch(t, tc.want, ids)
				})
			}
		})
	}
}

// 姓名、联系人、电话等 OR 后半分支也不能越过代理商归属和状态筛选。
func TestChildMerchantsKeywordKeepsOwnershipAndStatus(t *testing.T) {
	f := setup(t)
	agent := f.create(org.Agent(), "当前代理商", "admin", 0)
	other := f.create(org.Agent(), "其他代理商", "admin", 0)
	byName := f.create(org.Merchant(), "scopekeyword 名称", "admin", agent.Org.ID)
	byContact := f.create(org.Merchant(), "联系人商户", "admin", agent.Org.ID)
	byPhone := f.create(org.Merchant(), "电话商户", "admin", agent.Org.ID)
	foreign := f.create(org.Merchant(), "scopekeyword 外部商户", "admin", other.Org.ID)
	direct := f.create(org.Merchant(), "scopekeyword 直属商户", "admin", 0)
	require.NoError(t, f.svc.Update(f.ctx, org.Merchant(), byContact.Org.ID, org.UpdateInput{Name: byContact.Org.Name, ContactName: "scopekeyword"}, platformAdmin))
	require.NoError(t, f.svc.Update(f.ctx, org.Merchant(), byPhone.Org.ID, org.UpdateInput{Name: byPhone.Org.Name, ContactPhone: "scopekeyword"}, platformAdmin))
	for _, row := range []*org.Created{foreign, direct} {
		require.NoError(t, f.svc.Update(f.ctx, org.Merchant(), row.Org.ID, org.UpdateInput{Name: row.Org.Name, ContactName: "scopekeyword foreignonly", ContactPhone: "scopekeyword"}, platformAdmin))
	}
	require.NoError(t, f.svc.SetStatus(f.ctx, org.Merchant(), byPhone.Org.ID, org.StatusDisabled, platformAdmin))
	ctx := agentCtx(f.ctx, agent.Org.ID)
	enabled := org.StatusEnabled
	for _, tc := range []struct {
		name    string
		keyword string
		status  *int
		want    []uint64
	}{
		{"all matching local fields", "scopekeyword", nil, []uint64{byName.Org.ID, byContact.Org.ID, byPhone.Org.ID}},
		{"enabled only", "scopekeyword", &enabled, []uint64{byName.Org.ID, byContact.Org.ID}},
		{"local code", byContact.Org.Code, nil, []uint64{byContact.Org.ID}},
		{"foreign code", foreign.Org.Code, nil, nil},
		{"foreign contact", "foreignonly", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, total, err := f.svc.ChildMerchants(ctx, tc.status, httpx.PageQuery{Page: 1, PageSize: 20, Keyword: tc.keyword})
			require.NoError(t, err)
			require.EqualValues(t, len(tc.want), total)
			ids := make([]uint64, 0, len(rows))
			for _, row := range rows {
				if tc.status != nil {
					require.Equal(t, *tc.status, row.Status)
				}
				ids = append(ids, row.ID)
			}
			require.ElementsMatch(t, tc.want, ids)
		})
	}
}
