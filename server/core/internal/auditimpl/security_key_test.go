package auditimpl

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSecurity_195_NormalizationAndReservedBudget(t *testing.T) {
	now := time.Now()
	r := New(Options{DB: &gorm.DB{}, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now }, KnownPortal: func(p string) bool { return p == "platform" }})
	var anonymous, authenticated int64
	r.security.write = func(_ context.Context, _ securityRow, count int64) error { anonymous += count; return nil }
	r.authSecurity.write = func(_ context.Context, _ securityRow, count int64) error { authenticated += count; return nil }
	for _, ev := range []securityRow{
		{Kind: "ip_denied", Portal: "unknown-a", IP: "2001:db8::1", Method: "CUSTOMA", Path: "/unmatched/a"},
		{Kind: "ip_denied", Portal: "unknown-b", IP: "2001:db8::2", Method: "CUSTOMB", Path: "/unmatched/b"},
	} {
		r.addSecurity(context.Background(), ev)
	}
	require.Len(t, r.security.entries, 1)
	r.security.Flush(context.Background())
	require.Equal(t, int64(2), anonymous)
	r.security.newKeys.tokens = 0
	r.addSecurity(context.Background(), securityRow{Kind: "forbidden", Portal: "platform", IP: "192.0.2.1", Method: "GET", RouteKey: "/known", UserID: 8})
	require.Equal(t, int64(2), anonymous)
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{Portal: "platform", UserID: 8})
	r.addSecurity(ctx, securityRow{Kind: "forbidden", Portal: "platform", IP: "192.0.2.1", Method: "GET", RouteKey: "/known", UserID: 8})
	require.Equal(t, int64(1), authenticated)
}
