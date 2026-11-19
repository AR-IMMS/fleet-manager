// Package httpapi provides reusable HTTP response primitives for REST delivery adapters.
package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"

	"github.com/go-playground/validator/v10"
)

// Meta carries correlation metadata for a single REST response.
type Meta struct {
	RequestID string `json:"request_id" validate:"required,min=1,max=128"`
	TraceID   string `json:"trace_id,omitempty" validate:"omitempty,len=32,lowercase,hexadecimal"`
}

// SingleResponse wraps one result with response metadata.
type SingleResponse[T any] struct {
	Data T    `json:"data"`
	Meta Meta `json:"meta"`
}

// ListResponse wraps a collection with response metadata and optional pagination.
type ListResponse[T any] struct {
	Data       []T               `json:"data"`
	Meta       Meta              `json:"meta"`
	Pagination *CursorPagination `json:"pagination,omitempty"`
}

// CursorPagination describes an opaque cursor for the next collection page.
type CursorPagination struct {
	Type       string  `json:"type" validate:"required,eq=cursor"`
	NextCursor *string `json:"next_cursor"`
}

// FieldError describes one invalid field without including its submitted value.
type FieldError struct {
	Field   []any  `json:"field"`
	Code    string `json:"code" validate:"required,oneof=INVALID_TYPE INVALID_FORMAT OUT_OF_RANGE UNKNOWN_FIELD INVALID_VALUE"`
	Message string `json:"message"`
}

// Problem is the proposed API Problem Details response and Fleet extensions.
// Its instance and code values are supplied by the owning delivery adapter.
type Problem struct {
	Type      string       `json:"type"`
	Title     string       `json:"title"`
	Status    int          `json:"status"`
	Detail    string       `json:"detail"`
	Instance  string       `json:"instance"`
	Code      string       `json:"code"`
	RequestID string       `json:"request_id" validate:"required,min=1,max=128"`
	TraceID   string       `json:"trace_id,omitempty" validate:"omitempty,len=32,lowercase,hexadecimal"`
	Errors    []FieldError `json:"errors,omitempty"`
}

var errorCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)

// ValidateProblem checks the reusable Problem constraints in the baseline draft.
func ValidateProblem(problem Problem) error {
	validate := validator.New()
	if err := validate.Struct(problem); err != nil {
		return fmt.Errorf("invalid problem: %w", err)
	}
	if problem.Status < http.StatusBadRequest || problem.Status > 599 {
		return fmt.Errorf("problem status must be between 400 and 599")
	}
	if !errorCodePattern.MatchString(problem.Code) {
		return fmt.Errorf("problem code does not match the baseline format")
	}
	if _, err := url.Parse(problem.Type); err != nil {
		return fmt.Errorf("problem type must be a URI reference: %w", err)
	}
	if _, err := url.Parse(problem.Instance); err != nil {
		return fmt.Errorf("problem instance must be a URI reference: %w", err)
	}
	for i, fieldError := range problem.Errors {
		if fieldError.Field == nil {
			return fmt.Errorf("field error %d path must be an array", i)
		}
		if err := validate.Struct(fieldError); err != nil {
			return fmt.Errorf("invalid field error %d: %w", i, err)
		}
		for j, segment := range fieldError.Field {
			switch value := segment.(type) {
			case string:
				// Object property names are represented as strings.
			case int:
				if value < 0 {
					return fmt.Errorf("field error %d path segment %d has a negative index", i, j)
				}
			case int32:
				if value < 0 {
					return fmt.Errorf("field error %d path segment %d has a negative index", i, j)
				}
			case int64:
				if value < 0 {
					return fmt.Errorf("field error %d path segment %d has a negative index", i, j)
				}
			default:
				return fmt.Errorf("field error %d path segment %d must be a string or nonnegative integer", i, j)
			}
		}
	}
	return nil
}

// WriteJSON marshals the response completely before committing headers or status.
func WriteJSON(w http.ResponseWriter, status int, contentType string, value any) error {
	if status < 100 || status > 599 {
		return fmt.Errorf("invalid HTTP status %d", status)
	}
	if contentType == "" {
		return fmt.Errorf("content type is required")
	}
	body, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode HTTP response: %w", err)
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	n, err := w.Write(body)
	if err != nil {
		return fmt.Errorf("write HTTP response: %w", err)
	}
	if n != len(body) {
		return io.ErrShortWrite
	}
	return nil
}

// WriteProblem validates that the response and Problem statuses agree before writing.
func WriteProblem(w http.ResponseWriter, status int, problem Problem) error {
	if status != problem.Status {
		return fmt.Errorf("HTTP status %d does not match problem status %d", status, problem.Status)
	}
	if err := ValidateProblem(problem); err != nil {
		return err
	}
	return WriteJSON(w, status, "application/problem+json", problem)
}
