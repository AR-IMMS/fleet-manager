package mongodb

import (
	"fmt"
	"strings"
	"time"
)

// Options configures the Fleet MongoDB dependency.
//
// URI may contain credentials. Do not log or serialize this struct.
// Database selects application storage independently of URI authSource.
type Options struct {
	URI              string
	Database         string
	AppName          string
	ConnectTimeout   time.Duration
	OperationTimeout time.Duration
}

// Validate checks adapter inputs before creating a driver client.
//
// Driver-specific URI parsing is performed by Open. Validation errors
// intentionally omit connection strings and credential values.
func (o Options) Validate() error {
	if strings.TrimSpace(o.URI) == "" {
		return fmt.Errorf("MongoDB URI is required")
	}

	if o.Database == "" ||
		strings.ContainsAny(o.Database, "/\\ .\"$*<>:|?\x00") ||
		len(o.Database) >= 64 {
		return fmt.Errorf("MongoDB application database name is invalid")
	}

	switch strings.ToLower(o.Database) {
	case "admin", "config", "local":
		return fmt.Errorf("MongoDB requires an application database")
	}

	if strings.TrimSpace(o.AppName) == "" {
		return fmt.Errorf("MongoDB app name is required")
	}

	if o.ConnectTimeout <= 0 {
		return fmt.Errorf("MongoDB connection timeout must be positive")
	}

	if o.OperationTimeout <= 0 {
		return fmt.Errorf("MongoDB operation timeout must be positive")
	}

	return nil
}
