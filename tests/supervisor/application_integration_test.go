//go:build supervisor && integration

package supervisor_test

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AR-IMMS/fleet-manager/internal/app"
	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/domain"
	identitymongo "github.com/AR-IMMS/fleet-manager/internal/contexts/identity/infrastructure/mongodb"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"
	driver "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

func TestApplicationRunStartsAuthenticatedOpAMPAndShutsDown(t *testing.T) {
	mongoURI := requiredMongoURI(t)
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	client, database := createApplicationTestDatabase(t, ctx, mongoURI)
	databaseName := database.Name()
	if err := identitymongo.Migrate(ctx, database); err != nil {
		t.Fatal("migrate test identity database", err)
	}
	if err := identitymongo.EnsureIndexes(ctx, database); err != nil {
		t.Fatal("reconcile test identity indexes", err)
	}
	verifier, err := identitymongo.NewVerifier(database)
	if err != nil {
		t.Fatal("create verifier", err)
	}
	if err := verifier.StoreActiveCredential(ctx, testAgentID, testCredentialID, testCredential); err != nil {
		t.Fatal("seed test credential", err)
	}

	opampAddress := reserveLoopbackAddress(t)
	httpAddress := reserveLoopbackAddress(t)
	cfg := applicationIntegrationConfig(t, mongoURI, databaseName, opampAddress, httpAddress)
	application, err := app.NewApplication(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal("construct application", err)
	}

	runCtx, stopApplication := context.WithCancel(t.Context())
	runDone := make(chan error, 1)
	go func() {
		runDone <- application.Run(runCtx)
		close(runDone)
	}()
	t.Cleanup(func() {
		stopApplication()
		select {
		case err := <-runDone:
			if err != nil {
				t.Errorf("application cleanup failed: %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("application did not stop during test cleanup")
		}
	})

	waitApplicationReady(t, httpAddress, runDone)
	assertHealth(t, "http://"+httpAddress+"/health/live", http.StatusOK)
	assertHealth(t, "http://"+httpAddress+"/health/ready", http.StatusOK)

	endpoint := (&url.URL{Scheme: "ws", Host: opampAddress, Path: cfg.OpAMPPath}).String()
	supervisor := h.start(t, endpoint, filepath.Join(h.root, "application-state"))
	_ = supervisor
	uid := waitForMongoBinding(t, ctx, database.Collection("identity_installations"))
	decodedUID, err := hex.DecodeString(uid)
	if err != nil || len(decodedUID) != 16 {
		t.Fatalf("stored Supervisor UID is not a 16-byte UID: %q", uid)
	}
	if _, err := verifier.Verify(ctx, testCredential); err != nil {
		t.Fatalf("authenticated Supervisor credential was not accepted: %v", err)
	}

	stopApplication()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("application shutdown failed: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("application shutdown timed out")
	}

	assertAddressReleased(t, httpAddress)
	assertAddressReleased(t, opampAddress)
	if err := verifier.BindAndAuthorize(ctx, mustPrincipal(t), decodedUID); err != nil {
		t.Fatalf("shutdown changed the persisted authenticated UID binding: %v", err)
	}
	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		t.Fatalf("test Mongo client unexpectedly disconnected: %v", err)
	}
}

func TestApplicationRunCleansUpAfterOpAMPStartupFailure(t *testing.T) {
	mongoURI := requiredMongoURI(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	client, database := createApplicationTestDatabase(t, ctx, mongoURI)

	blockedListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("reserve blocked OpAMP address", err)
	}
	defer func() { _ = blockedListener.Close() }()
	opampAddress := blockedListener.Addr().String()
	httpAddress := reserveLoopbackAddress(t)
	application, err := app.NewApplication(
		applicationIntegrationConfig(t, mongoURI, database.Name(), opampAddress, httpAddress),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatal("construct application", err)
	}
	if err := application.Run(ctx); err == nil {
		t.Fatal("application must fail when its OpAMP listener cannot bind")
	}
	assertAddressReleased(t, httpAddress)
	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		t.Fatalf("test Mongo client unexpectedly disconnected: %v", err)
	}
}

func TestApplicationRunFailsBeforeListenersOnIdentityIndexConflict(t *testing.T) {
	mongoURI := requiredMongoURI(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	client, database := createApplicationTestDatabase(t, ctx, mongoURI)
	_, err := database.Collection("identity_installations").Indexes().CreateOne(ctx, driver.IndexModel{
		Keys:    bson.D{{Key: "credential_hash", Value: 1}},
		Options: options.Index().SetName("credential_hash_1"),
	})
	if err != nil {
		t.Fatal("create conflicting identity index", err)
	}

	httpAddress := reserveLoopbackAddress(t)
	cfg := applicationIntegrationConfig(t, mongoURI, database.Name(), "127.0.0.1:0", httpAddress)
	cfg.Enabled = false
	application, err := app.NewApplication(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal("construct application", err)
	}
	if err := application.Run(ctx); err == nil {
		t.Fatal("application must fail closed on an incompatible identity index")
	}
	assertAddressReleased(t, httpAddress)
	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		t.Fatalf("test Mongo client unexpectedly disconnected after application failure: %v", err)
	}
}

func requiredMongoURI(t *testing.T) string {
	t.Helper()
	uri := os.Getenv("FLEET_MONGODB_URI")
	if uri == "" {
		requireIntegrationDependency(t, "FLEET_MONGODB_URI is required for application wiring integration")
	}
	return uri
}

func createApplicationTestDatabase(t *testing.T, ctx context.Context, uri string) (*driver.Client, *driver.Database) {
	t.Helper()
	client, err := driver.Connect(options.Client().ApplyURI(uri).SetTimeout(5 * time.Second))
	if err != nil {
		t.Fatal("connect test MongoDB", err)
	}
	database := client.Database("fleet_application_test_" + uuid.NewString())
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if err := database.Drop(cleanupCtx); err != nil {
			t.Errorf("drop application test database: %v", err)
		}
		if err := client.Disconnect(cleanupCtx); err != nil {
			t.Errorf("disconnect application test Mongo client: %v", err)
		}
	})
	if err := client.Ping(ctx, nil); err != nil {
		t.Fatal("ping test MongoDB", err)
	}
	return client, database
}

func applicationIntegrationConfig(t *testing.T, mongoURI, databaseName, opampAddress, httpAddress string) app.Config {
	t.Helper()
	values := map[string]string{
		"FLEET_MONGODB_URI":         mongoURI,
		"FLEET_MONGODB_DATABASE":    databaseName,
		"FLEET_HTTP_LISTEN_ADDRESS": httpAddress,
		"OPAMP_ENABLED":             "true",
		"OPAMP_LISTEN_ADDRESS":      opampAddress,
	}
	cfg, err := app.LoadConfig(func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	if err != nil {
		t.Fatal("load application integration config", err)
	}
	return cfg
}

func reserveLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("reserve loopback address", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal("release loopback reservation", err)
	}
	return address
}

func waitApplicationReady(t *testing.T, address string, done <-chan error) {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("application exited before becoming ready: %v", err)
		default:
		}
		response, err := client.Get("http://" + address + "/health/ready")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("application did not report readiness")
}

func assertHealth(t *testing.T, endpoint string, expected int) {
	t.Helper()
	response, err := http.Get(endpoint)
	if err != nil {
		t.Fatalf("request health endpoint: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != expected {
		t.Fatalf("health endpoint returned %d, expected %d", response.StatusCode, expected)
	}
}

func waitForMongoBinding(t *testing.T, ctx context.Context, collection *driver.Collection) string {
	t.Helper()
	deadline := time.NewTicker(100 * time.Millisecond)
	defer deadline.Stop()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		var record bson.M
		err := collection.FindOne(ctx, bson.M{"agent_id": testAgentID, "credential_id": testCredentialID}).Decode(&record)
		if err == nil {
			if uid, ok := record["instance_uid"].(string); ok && uid != "" {
				return uid
			}
		} else if !errors.Is(err, driver.ErrNoDocuments) {
			t.Fatalf("read application UID binding: %v", err)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context ended waiting for Mongo UID binding: %v", ctx.Err())
		case <-timer.C:
			t.Fatal("timed out waiting for Application.Run to persist Supervisor UID")
		case <-deadline.C:
		}
	}
}

func mustPrincipal(t *testing.T) domain.Principal {
	t.Helper()
	principal, err := domain.NewPrincipal(testAgentID, testCredentialID)
	if err != nil {
		t.Fatal("construct expected principal", err)
	}
	return principal
}

func assertAddressReleased(t *testing.T, address string) {
	t.Helper()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("application leaked listener at %s: %v", address, err)
	}
	if err := listener.Close(); err != nil {
		t.Errorf("close listener check: %v", err)
	}
}
