package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/domain"
)

// SessionAuthorizer binds a Supervisor UID after authentication and fences
// every later message against current credential status and UID ownership.
type SessionAuthorizer interface {
	BindAndAuthorize(ctx context.Context, principal domain.Principal, uid []byte) error
}

// AuthorizeSupervisorMessage validates and authorizes an OpAMP message identity.
func AuthorizeSupervisorMessage(
	ctx context.Context,
	authorizer SessionAuthorizer,
	principal domain.Principal,
	uid []byte,
) error {
	if authorizer == nil || !principal.Valid() || len(uid) != 16 {
		return domain.ErrSessionUnauthorized
	}
	if err := authorizer.BindAndAuthorize(ctx, principal, append([]byte(nil), uid...)); err != nil {
		if errors.Is(err, domain.ErrUIDConflict) || errors.Is(err, domain.ErrSessionUnauthorized) {
			return err
		}
		return fmt.Errorf("authorize Supervisor session: %w", err)
	}
	return nil
}
