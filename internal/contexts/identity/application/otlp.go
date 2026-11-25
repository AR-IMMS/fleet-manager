package application

import (
	"context"

	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/domain"
)

// OTLPVerifier resolves a per-Agent bearer without issuing or caching it.
type OTLPVerifier interface {
	VerifyOTLP(context.Context, string) (domain.OTLPIdentity, error)
}
