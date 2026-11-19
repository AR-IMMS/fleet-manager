package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"unicode"

	"github.com/AR-IMMS/fleet-manager/internal/shared/correlation"
	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"
)

// RequestIDHeader is the response and optional request correlation header.
const RequestIDHeader = "X-Request-Id"

// IDGenerator creates a request correlation identifier.
type IDGenerator interface {
	NewID() (string, error)
}

type uuidGenerator struct{}

func (uuidGenerator) NewID() (string, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

// RequestIDFromContext returns the selected request ID, if the middleware set one.
func RequestIDFromContext(ctx context.Context) string {
	id, _ := correlation.RequestIDFromContext(ctx)
	return id
}

// RequestIDMiddleware selects a safe caller ID or generates one, then returns it in the response header.
// A nil generator uses random UUID generation; the identifier format remains an implementation choice.
func RequestIDMiddleware(generator IDGenerator) func(http.Handler) http.Handler {
	if generator == nil {
		generator = uuidGenerator{}
	}
	validate := validator.New()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			provided := r.Header.Values(RequestIDHeader)
			requestID, exists := correlation.RequestIDFromContext(r.Context())
			if exists {
				if !validRequestID(requestID, validate) {
					http.Error(w, "request could not be processed", http.StatusInternalServerError)
					return
				}
			} else if len(provided) == 1 && validRequestID(provided[0], validate) {
				requestID = provided[0]
			} else {
				generated, err := generator.NewID()
				if err != nil || !validRequestID(generated, validate) {
					// Do not expose random-source details or invent a problem code here.
					http.Error(w, "request could not be processed", http.StatusInternalServerError)
					return
				}
				requestID = generated
			}
			w.Header().Set(RequestIDHeader, requestID)
			ctx := correlation.WithRequestID(r.Context(), requestID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func validRequestID(id string, validate *validator.Validate) bool {
	if err := validate.Var(id, "required,min=1,max=128"); err != nil {
		return false
	}
	for _, r := range id {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// ValidateRequestID exposes the baseline length and safe-header checks to adapters.
func ValidateRequestID(id string) error {
	if !validRequestID(id, validator.New()) {
		return fmt.Errorf("request ID must contain 1 to 128 non-control characters")
	}
	return nil
}
