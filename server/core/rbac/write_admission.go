package rbac

import (
	"context"
	"fmt"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/internal/ratelimit"
)

type writeAdmissionKey struct {
	service *Service
	key     string
}

func (s *Service) withWriteAdmission(ctx context.Context, key string, gate *ratelimit.KeyedGate, fn func(context.Context) error) error {
	marker := writeAdmissionKey{s, key}
	if held, _ := ctx.Value(marker).(bool); held {
		return fn(ctx)
	}
	if !gate.TryEnter(key) {
		return httpx.ErrTooManyRequests
	}
	defer gate.Leave(key)
	if !s.allWrites.TryEnter() {
		return httpx.ErrTooManyRequests
	}
	defer s.allWrites.Leave()
	return fn(context.WithValue(ctx, marker, true))
}

func (s *Service) reserveRoleWrite(ctx context.Context, actor auth.Principal) error {
	org, err := s.actorOrg(actor)
	if err != nil {
		return err
	}
	if _, ok := s.roleWrites.Reserve(ctx, fmt.Sprintf("%s/%d", actor.Portal, org)); !ok {
		return httpx.ErrTooManyRequests
	}
	return nil
}
