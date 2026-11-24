package domain

import "time"

// OTLPIdentity is the secret-free identity of an active telemetry credential.
type OTLPIdentity struct {
	AgentID      string
	CredentialID string
	Scope        string
	ExpiresAt    *time.Time
}
