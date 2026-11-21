package opamp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strings"

	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/application"
	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/domain"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/server/types"
)

// ConnectionAuthenticator is the delivery-owned authentication dependency.
// Implementations must honor cancellation and return safe application error classifications.
type ConnectionAuthenticator interface {
	Authenticate(context.Context, string) (domain.Principal, error)
}

// CallbackAdapter translates OpAMP connection callbacks into application operations.
//
// It contains no Fleet registration or approval policy. The authenticator
// resolves connection authorization through its injected identity verifier.
type CallbackAdapter struct {
	authenticator ConnectionAuthenticator
	authorizer    application.SessionAuthorizer
	logger        *slog.Logger
}

// NewCallbackAdapter constructs an OpAMP callback adapter.
//
// Dependencies are mandatory so that missing authentication or logging wiring
// causes startup to fail rather than silently changing connection behavior.
func NewCallbackAdapter(
	authenticator ConnectionAuthenticator,
	authorizer application.SessionAuthorizer,
	logger *slog.Logger,
) (*CallbackAdapter, error) {
	if authenticator == nil || (reflect.ValueOf(authenticator).Kind() == reflect.Pointer && reflect.ValueOf(authenticator).IsNil()) {
		return nil, fmt.Errorf("authenticator is required")
	}

	if logger == nil {
		return nil, fmt.Errorf("logger is required")
	}
	if authorizer == nil || (reflect.ValueOf(authorizer).Kind() == reflect.Pointer && reflect.ValueOf(authorizer).IsNil()) {
		return nil, fmt.Errorf("session authorizer is required")
	}

	return &CallbackAdapter{
		authenticator: authenticator,
		authorizer:    authorizer,
		logger:        logger,
	}, nil
}

// Callbacks returns the SDK entry points owned by this adapter.
//
// Authentication occurs before the SDK accepts the protocol connection.
func (h *CallbackAdapter) Callbacks() types.Callbacks {
	return types.Callbacks{
		OnConnecting: h.onConnecting,
	}
}

// onConnecting authenticates an incoming request before admitting its connection.
//
// Credentials and raw verifier errors are intentionally excluded from responses
// and logs. A temporary verification failure must not admit an unknown identity.
func (h *CallbackAdapter) onConnecting(r *http.Request) types.ConnectionResponse {
	credential, ok := bearerCredential(r.Header)
	if !ok {
		return unauthorized()
	}

	principal, err := h.authenticator.Authenticate(r.Context(), credential)
	if err != nil {
		if errors.Is(err, application.ErrUnauthenticated) {
			return unauthorized()
		}

		h.logger.WarnContext(
			r.Context(),
			"OpAMP connection rejected: identity verification unavailable",
		)

		return types.ConnectionResponse{
			Accept:         false,
			HTTPStatusCode: http.StatusServiceUnavailable,
		}
	}

	return types.ConnectionResponse{
		Accept:              true,
		ConnectionCallbacks: h.connectionCallbacks(principal),
	}
}

// connectionCallbacks creates handlers bound to one authenticated identity.
//
// The principal is captured directly rather than recovered from client-reported
// metadata. Connection callbacks do not establish global session ownership yet.
func (h *CallbackAdapter) connectionCallbacks(
	principal domain.Principal,
) types.ConnectionCallbacks {
	logger := h.logger.With("installation_id", principal.InstallationID())

	return types.ConnectionCallbacks{
		OnConnected: func(ctx context.Context, _ types.Connection) {
			logger.InfoContext(ctx, "OpAMP connection established")
		},

		OnMessage: func(
			ctx context.Context,
			conn types.Connection,
			message *protobufs.AgentToServer,
		) *protobufs.ServerToAgent {
			// OpAMP instance identifiers contain exactly 16 bytes. An invalid
			// identifier cannot be used to correlate a protocol response.
			if message == nil || len(message.InstanceUid) != 16 {
				logger.WarnContext(ctx, "OpAMP message rejected: invalid instance UID")

				if err := conn.Disconnect(); err != nil {
					logger.DebugContext(ctx, "OpAMP connection close returned an error")
				}

				return nil
			}
			if err := application.AuthorizeSupervisorMessage(ctx, h.authorizer, principal, message.InstanceUid); err != nil {
				if errors.Is(err, domain.ErrUIDConflict) || errors.Is(err, domain.ErrSessionUnauthorized) {
					logger.WarnContext(ctx, "OpAMP message rejected: identity binding or authorization conflict")
				} else {
					logger.WarnContext(ctx, "OpAMP message rejected: identity store unavailable")
				}
				if disconnectErr := conn.Disconnect(); disconnectErr != nil {
					logger.DebugContext(ctx, "OpAMP connection close returned an error")
				}
				return nil
			}

			// This bootstrap response advertises no server capabilities.
			// Status acceptance and remote configuration are introduced only
			// when their application behavior and tests exist.
			return &protobufs.ServerToAgent{
				InstanceUid: append([]byte(nil), message.InstanceUid...),
			}
		},

		OnConnectionClose: func(_ types.Connection) {
			logger.Info("OpAMP connection closed")
		},

		OnReadMessageError: func(
			_ types.Connection,
			_ int,
			_ []byte,
			_ error,
		) {
			// Raw message bytes and parser errors may contain sensitive input.
			logger.Warn("OpAMP message could not be decoded")
		},

		OnMessageResponseError: func(
			_ types.Connection,
			_ *protobufs.ServerToAgent,
			_ error,
		) {
			logger.Warn("OpAMP response could not be sent")
		},
	}
}

// bearerCredential accepts exactly one Authorization header with a Bearer token.
//
// It rejects ambiguous or whitespace-separated credentials. Credential validity
// remains the identity verifier's responsibility.
func bearerCredential(header http.Header) (string, bool) {
	values := header.Values("Authorization")
	if len(values) != 1 {
		return "", false
	}

	parts := strings.Fields(values[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", false
	}

	if strings.Contains(parts[1], ",") {
		return "", false
	}

	return parts[1], true
}

// unauthorized returns the same response for missing and rejected credentials.
//
// A uniform response avoids revealing whether a particular installation exists.
func unauthorized() types.ConnectionResponse {
	return types.ConnectionResponse{
		Accept:         false,
		HTTPStatusCode: http.StatusUnauthorized,
		HTTPResponseHeader: map[string]string{
			"WWW-Authenticate": "Bearer",
		},
	}
}
