package mongodb

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

var (
	// ErrConfiguration means the driver rejected connection options.
	ErrConfiguration = errors.New("MongoDB connection configuration rejected")

	// ErrUnavailable means a connection or health check could not succeed.
	ErrUnavailable = errors.New("MongoDB dependency unavailable")

	// ErrClose means client cleanup did not complete successfully.
	ErrClose = errors.New("MongoDB client cleanup failed")
)

// Database owns one reusable driver client and its application database.
//
// Construct it through Open. Application composition owns Close; repositories
// receive Handle and must not disconnect the shared client.
type Database struct {
	client           *mongo.Client
	handle           *mongo.Database
	operationTimeout time.Duration
}

// Open creates a client and verifies primary connectivity before returning.
//
// ConnectTimeout bounds initial network verification. Failed startup closes
// the client using an independent cleanup context, even if ctx was cancelled.
// Creating the database handle does not create collections or write records.
func Open(ctx context.Context, cfg Options) (*Database, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	if err := ctx.Err(); err != nil {
		return nil, errors.Join(ErrUnavailable, err)
	}

	clientOptions := options.Client().
		ApplyURI(cfg.URI).
		SetAppName(cfg.AppName).
		SetConnectTimeout(cfg.ConnectTimeout).
		SetServerSelectionTimeout(cfg.ConnectTimeout).
		SetTimeout(cfg.OperationTimeout)

	client, err := mongo.Connect(clientOptions)
	if err != nil {
		return nil, ErrConfiguration
	}

	connectCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	err = client.Ping(connectCtx, readpref.Primary())

	var openErr error
	if err != nil {
		openErr = safeError(ErrUnavailable, connectCtx, err)
	}

	cancel()

	if openErr != nil {
		if cleanupErr := closeFailedClient(client, cfg.ConnectTimeout); cleanupErr != nil {
			return nil, errors.Join(openErr, cleanupErr)
		}

		return nil, openErr
	}

	return &Database{
		client:           client,
		handle:           client.Database(cfg.Database),
		operationTimeout: cfg.OperationTimeout,
	}, nil
}

// Handle exposes the selected database to infrastructure repositories.
//
// Domain and application packages must not depend on this driver type.
func (d *Database) Handle() *mongo.Database {
	return d.handle
}

// Ping verifies primary availability within the configured operation timeout.
//
// An earlier caller deadline takes precedence. Success proves connectivity,
// not application collection permissions or transaction support.
func (d *Database) Ping(ctx context.Context) error {
	checkCtx, cancel := context.WithTimeout(ctx, d.operationTimeout)
	defer cancel()

	if err := d.client.Ping(checkCtx, readpref.Primary()); err != nil {
		return safeError(ErrUnavailable, checkCtx, err)
	}

	return nil
}

// Close disconnects the shared client using the caller's cleanup deadline.
//
// Composition must pass a fresh, bounded shutdown context rather than the
// cancelled runtime context. Close is called after repository workers stop.
func (d *Database) Close(ctx context.Context) error {
	if err := d.client.Disconnect(ctx); err != nil {
		return safeError(ErrClose, ctx, err)
	}

	return nil
}

// closeFailedClient releases resources allocated during unsuccessful startup.
func closeFailedClient(client *mongo.Client, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := client.Disconnect(ctx); err != nil {
		return safeError(ErrClose, ctx, err)
	}

	return nil
}

// safeError preserves cancellation classification without exposing driver text.
func safeError(category error, ctx context.Context, cause error) error {
	if errors.Is(cause, context.DeadlineExceeded) {
		return errors.Join(category, context.DeadlineExceeded)
	}

	if errors.Is(cause, context.Canceled) {
		return errors.Join(category, context.Canceled)
	}

	if err := ctx.Err(); err != nil {
		return errors.Join(category, err)
	}

	return category
}
