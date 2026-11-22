//go:build integration

package mongodb

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/domain"
	"go.mongodb.org/mongo-driver/v2/bson"
	driver "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func TestVerifierBindingAndRevocationFence(t *testing.T) {
	uri := os.Getenv("FLEET_MONGODB_URI")
	if uri == "" {
		uri = os.Getenv("MONGODB_URI")
	}
	if uri == "" {
		requireIntegrationDependency(t, "set FLEET_MONGODB_URI or MONGODB_URI to run Mongo identity integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client, err := driver.Connect(options.Client().ApplyURI(uri).SetTimeout(5 * time.Second))
	if err != nil {
		t.Fatal("connect MongoDB")
	}
	defer func() { _ = client.Disconnect(context.Background()) }()
	db := client.Database("ar_imms_fleet_identity_test")
	_ = db.Collection(collectionName).Drop(ctx)
	_ = db.Collection(migrationsCollectionName).Drop(ctx)
	t.Cleanup(func() {
		_ = db.Collection(collectionName).Drop(context.Background())
		_ = db.Collection(migrationsCollectionName).Drop(context.Background())
	})
	if err := Migrate(ctx, db); err != nil {
		t.Fatal("migrate identity schema", err)
	}
	if err := EnsureIndexes(ctx, db); err != nil {
		t.Fatal("reconcile identity indexes", err)
	}
	verifier, err := NewVerifier(db)
	if err != nil {
		t.Fatal("create verifier", err)
	}
	seed := func(agentID, credentialID, credential string) domain.Principal {
		t.Helper()
		if storeErr := verifier.StoreActiveCredential(ctx, agentID, credentialID, credential); storeErr != nil {
			t.Fatal("seed credential record", storeErr)
		}
		principal, verifyErr := verifier.Verify(ctx, credential)
		if verifyErr != nil {
			t.Fatal("verify seeded credential", verifyErr)
		}
		return principal
	}

	first := seed("agent-a", "credential-a", "token-a-0123456789012345678901234567890123456")
	uid := []byte("0123456789abcdef")
	if err := verifier.BindAndAuthorize(ctx, first, uid); err != nil {
		t.Fatalf("first binding rejected: %v", err)
	}
	if err := verifier.BindAndAuthorize(ctx, first, uid); err != nil {
		t.Fatalf("same UID reconnect rejected: %v", err)
	}
	if err := verifier.BindAndAuthorize(ctx, first, []byte("fedcba9876543210")); !errors.Is(err, domain.ErrUIDConflict) {
		t.Fatalf("different UID rebind should conflict, got %v", err)
	}

	second := seed("agent-b", "credential-b", "token-b-0123456789012345678901234567890123456")
	if err := verifier.BindAndAuthorize(ctx, second, uid); !errors.Is(err, domain.ErrUIDConflict) {
		t.Fatalf("UID shared by distinct installations should conflict, got %v", err)
	}

	third := seed("agent-c", "credential-c", "token-c-0123456789012345678901234567890123456")
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = verifier.BindAndAuthorize(ctx, third, []byte("aaaaaaaaaaaaaaaa")) }()
	go func() { defer wg.Done(); _ = verifier.Revoke(ctx, third.CredentialID()) }()
	wg.Wait()
	if err := verifier.BindAndAuthorize(ctx, third, []byte("aaaaaaaaaaaaaaaa")); !errors.Is(err, domain.ErrSessionUnauthorized) {
		t.Fatalf("post-revocation session check should be denied, got %v", err)
	}
	if _, err := verifier.Verify(ctx, "token-c-0123456789012345678901234567890123456"); !errors.Is(err, domain.ErrCredentialRejected) {
		t.Fatalf("revoked credential should not authenticate, got %v", err)
	}
	var stored bson.M
	if err := db.Collection(collectionName).FindOne(ctx, bson.M{"credential_id": "credential-a"}).Decode(&stored); err != nil {
		t.Fatal("load persisted credential", err)
	}
	if stored["credential_hash"] == "token-a-0123456789012345678901234567890123456" {
		t.Fatal("raw credential was persisted")
	}
}

func requireIntegrationDependency(t *testing.T, message string) {
	t.Helper()
	if os.Getenv("FLEET_REQUIRE_INTEGRATION") == "true" {
		t.Fatal(message)
	}
	t.Skip(message)
}
