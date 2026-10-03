package org_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/org"
)

func TestBriefOptions_194_PublicFields(t *testing.T) {
	f := setup(t)
	a := f.create(org.Agent(), "Visible agency", "owner", 0)
	require.NoError(t, f.gdb.Table("ga_agent").Where("id = ?", a.Org.ID).Updates(map[string]any{"contact_name": "private-contact-marker", "contact_phone": "5554321987"}).Error)
	for _, keyword := range []string{"private-contact-marker", "5554321987"} {
		rows, err := f.svc.BriefOptions(f.ctx, org.Agent(), keyword)
		require.NoError(t, err)
		require.Empty(t, rows)
	}
	rows, err := f.svc.BriefOptions(f.ctx, org.Agent(), "Visible")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, a.Org.ID, rows[0].ID)
}
