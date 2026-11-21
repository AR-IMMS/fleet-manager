// Package correlation carries opaque request correlation metadata across layers.
package correlation

import "context"

type requestIDKey struct{}

// WithRequestID stores the adapter-selected value without validation or generation.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestIDFromContext reports a nonempty ID, if one was selected upstream.
func RequestIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(requestIDKey{}).(string)
	return id, ok && id != ""
}
