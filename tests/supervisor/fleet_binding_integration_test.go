//go:build supervisor && integration

package supervisor_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/domain"
	identitymongo "github.com/AR-IMMS/fleet-manager/internal/contexts/identity/infrastructure/mongodb"
	"github.com/google/uuid"
	driver "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func TestSupervisorFleetUIDBindingAndFreshStateConflict(t *testing.T) {
	uri := os.Getenv("FLEET_MONGODB_URI")
	if uri == "" {
		t.Skip("FLEET_MONGODB_URI is required for live Fleet/Mongo Supervisor acceptance")
	}
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	client, err := driver.Connect(options.Client().ApplyURI(uri).SetTimeout(5 * time.Second))
	if err != nil {
		t.Fatal("connect MongoDB", err)
	}
	databaseName := "fleet_supervisor_test_" + uuid.NewString()
	database := client.Database(databaseName)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if err := database.Drop(cleanupCtx); err != nil {
			t.Errorf("drop dedicated Fleet integration database: %v", err)
		}
		if err := client.Disconnect(cleanupCtx); err != nil {
			t.Errorf("disconnect MongoDB test client: %v", err)
		}
	})
	verifier, err := identitymongo.NewVerifier(ctx, database)
	if err != nil {
		t.Fatal("create Mongo identity verifier", err)
	}
	if err := verifier.StoreActiveCredential(ctx, testAgentID, testCredentialID, testCredential); err != nil {
		t.Fatal("store test-only Fleet credential", err)
	}
	principal, err := verifier.Verify(ctx, testCredential)
	if err != nil {
		t.Fatal("verify test-only Fleet credential", err)
	}
	server, events := h.server(t, verifier)
	retainedDir := filepath.Join(h.root, "fleet-retained-state")
	firstProcess := h.start(t, h.endpoint(server), retainedDir)
	first := h.waitUID(events)
	if first.err != nil {
		t.Fatalf("Fleet rejected first authenticated UID: %v", first.err)
	}
	firstProcess.stop()
	drainUIDEvents(events)

	secondProcess := h.start(t, h.endpoint(server), retainedDir)
	second := h.waitUID(events)
	if second.err != nil || !bytes.Equal(first.uid, second.uid) {
		t.Fatalf("Fleet did not accept same-state reconnect (first=%x second=%x error=%v)", first.uid, second.uid, second.err)
	}
	secondProcess.stop()
	drainUIDEvents(events)

	thirdProcess := h.start(t, h.endpoint(server), filepath.Join(h.root, "fleet-fresh-state"))
	third := h.waitUID(events)
	if bytes.Equal(first.uid, third.uid) {
		t.Fatalf("fresh Supervisor state did not produce a new UID: %x", first.uid)
	}
	if !errors.Is(third.err, domain.ErrUIDConflict) {
		t.Fatalf("Fleet should reject a new UID without remapping; got %v", third.err)
	}
	thirdProcess.stop()
	if err := verifier.BindAndAuthorize(ctx, principal, first.uid); err != nil {
		t.Fatalf("original binding was overwritten after fresh-state reconnect: %v", err)
	}
	if err := verifier.BindAndAuthorize(ctx, principal, third.uid); !errors.Is(err, domain.ErrUIDConflict) {
		t.Fatalf("fresh-state UID should remain conflicting, got %v", err)
	}
	t.Logf("Fleet/Mongo binding accepted reconnect uid=%x and rejected fresh-state uid=%x", first.uid, third.uid)
}
