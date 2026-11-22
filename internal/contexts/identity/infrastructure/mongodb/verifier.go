package mongodb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/domain"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

const collectionName = "identity_installations"

var errRecordNotFound = errors.New("identity record not found")
var errInvalidCredentialRecord = errors.New("identity credential record is invalid")

type record struct {
	AgentID        string `bson:"agent_id"`
	NodeID         string `bson:"node_id,omitempty"`
	CredentialID   string `bson:"credential_id"`
	CredentialHash string `bson:"credential_hash"`
	Status         string `bson:"status"`
	InstanceUID    string `bson:"instance_uid,omitempty"`
}

// Verifier checks Fleet-issued bearer credentials and atomically owns Supervisor
// instance UID binding. One Mongo document is the concurrency boundary for both.
type Verifier struct {
	collection *mongo.Collection
}

// StoreActiveCredential persists an enrollment-issued token digest and its
// authorized agent mapping. The enrollment result must be returned separately
// to its one-time recipient; raw credentials are never persisted here.
func (v *Verifier) StoreActiveCredential(
	ctx context.Context,
	agentID, credentialID, credential string,
) error {
	return v.storeCredentialWithContext(ctx, agentID, credentialID, credential)
}

func (v *Verifier) storeCredentialWithContext(
	ctx context.Context,
	agentID, credentialID, credential string,
) error {
	return v.storeCredentialForNode(ctx, agentID, "", credentialID, credential)
}

func (v *Verifier) storeCredentialForNode(
	ctx context.Context,
	agentID, nodeID, credentialID, credential string,
) error {
	principal, err := domain.NewPrincipal(agentID, credentialID)
	if err != nil || credential == "" || len(credential) < 32 || (nodeID != "" && nodeID != strings.TrimSpace(nodeID)) {
		return errInvalidCredentialRecord
	}
	_, err = v.collection.InsertOne(ctx, record{
		AgentID: principal.InstallationID(), NodeID: nodeID, CredentialID: principal.CredentialID(),
		CredentialHash: digest(credential), Status: "ACTIVE",
	})
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return domain.ErrEnrollmentConflict
		}
		return domain.ErrIdentityUnavailable
	}
	return nil
}

// NewVerifier creates a verifier over the identity collection. Call Migrate and
// EnsureIndexes during application startup before accepting requests.
func NewVerifier(database *mongo.Database) (*Verifier, error) {
	if database == nil {
		return nil, fmt.Errorf("identity database is required")
	}
	return &Verifier{collection: database.Collection(collectionName)}, nil
}

// Verify resolves an active high-entropy credential without retaining or logging it.
func (v *Verifier) Verify(ctx context.Context, credential string) (domain.Principal, error) {
	if len(credential) < 32 {
		return domain.Principal{}, domain.ErrCredentialRejected
	}
	var found record
	err := v.collection.FindOne(ctx, bson.M{
		"credential_hash": digest(credential),
		"status":          "ACTIVE",
	}).Decode(&found)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return domain.Principal{}, domain.ErrCredentialRejected
	}
	if err != nil {
		return domain.Principal{}, domain.ErrIdentityUnavailable
	}
	principal, err := domain.NewPrincipal(found.AgentID, found.CredentialID)
	if err != nil {
		return domain.Principal{}, domain.ErrIdentityUnavailable
	}
	return principal, nil
}

// BindAndAuthorize uses a conditional single-document update: the first UID is
// pinned, reconnects must present that same UID, and revoked records cannot pass.
func (v *Verifier) BindAndAuthorize(
	ctx context.Context,
	principal domain.Principal,
	uid []byte,
) error {
	if !principal.Valid() || len(uid) != 16 {
		return domain.ErrSessionUnauthorized
	}
	uidText := hex.EncodeToString(uid)
	filter := bson.M{
		"agent_id": principal.InstallationID(), "credential_id": principal.CredentialID(),
		"status": "ACTIVE", "$or": bson.A{
			bson.M{"instance_uid": bson.M{"$exists": false}},
			bson.M{"instance_uid": uidText},
		},
	}
	result, err := v.collection.UpdateOne(ctx, filter, bson.M{"$set": bson.M{"instance_uid": uidText}})
	if err == nil {
		if result.MatchedCount == 1 {
			return nil
		}
		var current record
		lookupErr := v.collection.FindOne(ctx, bson.M{
			"agent_id": principal.InstallationID(), "credential_id": principal.CredentialID(),
		}).Decode(&current)
		if errors.Is(lookupErr, mongo.ErrNoDocuments) || current.Status != "ACTIVE" {
			return domain.ErrSessionUnauthorized
		}
		if lookupErr != nil {
			return domain.ErrIdentityUnavailable
		}
		if current.InstanceUID != uidText {
			return domain.ErrUIDConflict
		}
		return domain.ErrIdentityUnavailable
	}
	var writeErr mongo.WriteException
	if errors.As(err, &writeErr) {
		for _, item := range writeErr.WriteErrors {
			if item.Code == 11000 {
				return domain.ErrUIDConflict
			}
		}
	}
	return domain.ErrIdentityUnavailable
}

// Revoke atomically fences reconnects and subsequent callback checks.
func (v *Verifier) Revoke(ctx context.Context, credentialID string) error {
	result, err := v.collection.UpdateOne(ctx,
		bson.M{"credential_id": credentialID, "status": "ACTIVE"},
		bson.M{"$set": bson.M{"status": "REVOKED"}},
	)
	if err != nil {
		return domain.ErrIdentityUnavailable
	}
	if result.MatchedCount == 0 {
		return errRecordNotFound
	}
	return nil
}

func digest(credential string) string {
	sum := sha256.Sum256([]byte(credential))
	return hex.EncodeToString(sum[:])
}
