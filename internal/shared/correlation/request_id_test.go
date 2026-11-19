package correlation

import (
	"context"
	"testing"
)

func TestContextPropagation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	child := WithRequestID(parent, "opaque")
	if id, ok := RequestIDFromContext(child); !ok || id != "opaque" {
		t.Fatal(id, ok)
	}
	if _, ok := RequestIDFromContext(parent); ok {
		t.Fatal("parent mutated")
	}
	if _, ok := RequestIDFromContext(WithRequestID(child, "")); ok {
		t.Fatal("empty ID present")
	}
	cancel()
	if child.Err() != context.Canceled {
		t.Fatal("cancellation lost")
	}
}
