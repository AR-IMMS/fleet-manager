package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fixedIDGenerator struct {
	id  string
	err error
}

func (g fixedIDGenerator) NewID() (string, error) { return g.id, g.err }

func TestRequestIDMiddlewareValidatesAndReturnsHeader(t *testing.T) {
	tests := []struct {
		name      string
		provided  string
		generated string
		want      string
	}{
		{name: "accepts opaque safe caller value", provided: "client-id_1", generated: "generated", want: "client-id_1"},
		{name: "replaces invalid caller value", provided: "invalid\r\nX-Injected: yes", generated: "generated", want: "generated"},
		{name: "generates when absent", generated: "generated", want: "generated"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := RequestIDMiddleware(fixedIDGenerator{id: tt.generated})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := RequestIDFromContext(r.Context()); got != tt.want {
					t.Errorf("context request ID = %q, want %q", got, tt.want)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.provided != "" {
				req.Header.Set(RequestIDHeader, tt.provided)
			}
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)
			if got := res.Header().Get(RequestIDHeader); got != tt.want {
				t.Errorf("response request ID = %q, want %q", got, tt.want)
			}
			if res.Code != http.StatusNoContent {
				t.Errorf("status = %d, want %d", res.Code, http.StatusNoContent)
			}
		})
	}
}

func TestRequestIDMiddlewareDoesNotExposeGeneratorError(t *testing.T) {
	secret := errors.New("private entropy source detail")
	handler := RequestIDMiddleware(fixedIDGenerator{err: secret})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("next handler called")
	}))
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/", nil))
	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", res.Code)
	}
	if strings.Contains(res.Body.String(), secret.Error()) {
		t.Fatal("generator error leaked in response")
	}
}

func TestWriteJSONEncodesBeforeWritingHeaders(t *testing.T) {
	res := httptest.NewRecorder()
	res.Header().Set("X-Before", "preserved")
	err := WriteJSON(res, http.StatusOK, "application/json", make(chan int))
	if err == nil {
		t.Fatal("WriteJSON() error = nil, want serialization error")
	}
	if res.Code != http.StatusOK || res.Body.Len() != 0 {
		t.Fatalf("response changed before encode succeeded: status=%d body=%q", res.Code, res.Body.String())
	}
	if got := res.Header().Get("Content-Type"); got != "" {
		t.Fatalf("Content-Type set before encoding: %q", got)
	}
}

func TestWriteProblemValidatesStatusAndFieldPaths(t *testing.T) {
	problem := Problem{
		Type: "about:blank", Title: "Invalid request", Status: http.StatusBadRequest,
		Detail: "One or more fields are invalid.", Instance: "", Code: "VALIDATION_ERROR", RequestID: "opaque-id",
		Errors: []FieldError{{Field: []any{"items", 0, "name"}, Code: "INVALID_FORMAT", Message: "Must be valid."}},
	}
	res := httptest.NewRecorder()
	if err := WriteProblem(res, http.StatusBadRequest, problem); err != nil {
		t.Fatalf("WriteProblem() error = %v", err)
	}
	if res.Code != problem.Status {
		t.Fatalf("HTTP status = %d, body status = %d", res.Code, problem.Status)
	}
	if got := res.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("Content-Type = %q", got)
	}

	problem.Status = http.StatusConflict
	res = httptest.NewRecorder()
	if err := WriteProblem(res, http.StatusBadRequest, problem); err == nil {
		t.Fatal("WriteProblem() accepted mismatched status")
	}
	if res.Code != http.StatusOK || res.Body.Len() != 0 {
		t.Fatalf("invalid problem wrote response: status=%d body=%q", res.Code, res.Body.String())
	}

	problem.Status = http.StatusBadRequest
	problem.Errors[0].Field = []any{"items", -1}
	if err := ValidateProblem(problem); err == nil {
		t.Fatal("ValidateProblem() accepted a negative array index")
	}
	problem.Errors[0].Field = nil
	if err := ValidateProblem(problem); err == nil {
		t.Fatal("ValidateProblem() accepted a null field path")
	}
}

func TestListResponseKeepsNullNextCursor(t *testing.T) {
	res := httptest.NewRecorder()
	response := ListResponse[string]{Data: []string{}, Meta: Meta{RequestID: "opaque"}, Pagination: &CursorPagination{Type: "cursor"}}
	if err := WriteJSON(res, http.StatusOK, "application/json", response); err != nil {
		t.Fatalf("WriteJSON() error = %v", err)
	}
	if !strings.Contains(res.Body.String(), `"next_cursor":null`) {
		t.Fatalf("next_cursor not serialized as null: %s", res.Body.String())
	}
}

func TestRequestIDFromContextEmptyWhenUnset(t *testing.T) {
	if got := RequestIDFromContext(context.Background()); got != "" {
		t.Fatalf("RequestIDFromContext() = %q, want empty", got)
	}
}
