//go:build integration

package mongodb

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func integrationMongoURI() string {
	if uri := os.Getenv("FLEET_MONGODB_URI"); uri != "" {
		return uri
	}
	if uri := os.Getenv("MONGODB_URI"); uri != "" {
		return uri
	}
	return "mongodb://127.0.0.1:27017"
}

func TestDatabase_OpenPingHandleClose(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db, err := Open(ctx, Options{
		URI:              integrationMongoURI(),
		Database:         "ar_imms_fleet_test",
		AppName:          "ar-imms-fleet-manager-test",
		ConnectTimeout:   3 * time.Second,
		OperationTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("Open failed (is MongoDB running?): %v", err)
	}

	if db.Handle() == nil {
		t.Fatal("expected non-nil database handle")
	}
	if got := db.Handle().Name(); got != "ar_imms_fleet_test" {
		t.Fatalf("unexpected database name: got %q, want %q", got, "ar_imms_fleet_test")
	}

	if err := db.Ping(ctx); err != nil {
		t.Fatalf("Ping failed: %v", err)
	}

	closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer closeCancel()

	if err := db.Close(closeCtx); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

func TestDatabase_OpenFailsWhenUnreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Port 1 trên loopback thường từ chối kết nối ngay hoặc timeout nhanh
	_, err := Open(ctx, Options{
		URI:              "mongodb://127.0.0.1:1",
		Database:         "ar_imms_fleet_test",
		AppName:          "ar-imms-fleet-manager-test",
		ConnectTimeout:   500 * time.Millisecond,
		OperationTimeout: 500 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected Open to fail against unreachable address")
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}
