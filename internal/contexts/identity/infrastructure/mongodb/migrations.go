package mongodb

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	migrationsCollectionName      = "identity_schema_migrations"
	identityBaselineMigrationID   = "identity-installations-v1"
	identityBaselineMigrationHash = "f83499150e04c4cbab315a6758942c8a3fb7497252101198c0ff25b17f789e43"
)

type migrationRecord struct {
	ID        string    `bson:"_id"`
	Checksum  string    `bson:"checksum"`
	AppliedAt time.Time `bson:"applied_at"`
}

// Migrate applies immutable versioned identity schema migrations and records
// each successful version. Index reconciliation is deliberately separate.
func Migrate(ctx context.Context, database *mongo.Database) error {
	if database == nil {
		return errors.New("identity migration database is required")
	}
	if err := ensureCollection(ctx, database, migrationsCollectionName); err != nil {
		return errors.New("identity migration history unavailable")
	}
	if err := validateMigrationHistory(ctx, database); err != nil {
		return err
	}
	return applyBaselineMigration(ctx, database)
}

// EnsureIndexes reconciles the indexes required by the identity verifier on
// every startup, independently of the versioned migration history.
func EnsureIndexes(ctx context.Context, database *mongo.Database) error {
	if database == nil {
		return errors.New("identity index database is required")
	}
	_, err := database.Collection(collectionName).Indexes().CreateMany(ctx, identityIndexes())
	if err != nil {
		return errors.New("identity verifier indexes unavailable")
	}
	return nil
}

func applyBaselineMigration(ctx context.Context, database *mongo.Database) error {
	collection := database.Collection(migrationsCollectionName)
	var applied migrationRecord
	err := collection.FindOne(ctx, bson.M{"_id": identityBaselineMigrationID}).Decode(&applied)
	if err == nil {
		return validateMigrationRecord(applied)
	}
	if !errors.Is(err, mongo.ErrNoDocuments) {
		return errors.New("identity migration history unavailable")
	}

	// This baseline migration only ensures the identity collection exists, so
	// concurrent first starts and retry after interruption are safe.
	if err := ensureCollection(ctx, database, collectionName); err != nil {
		return errors.New("identity baseline migration failed")
	}
	record := migrationRecord{
		ID: identityBaselineMigrationID, Checksum: identityBaselineMigrationHash,
		AppliedAt: time.Now().UTC(),
	}
	if _, err := collection.InsertOne(ctx, record); err != nil {
		if !mongo.IsDuplicateKeyError(err) {
			return errors.New("identity migration history unavailable")
		}
		var concurrentRecord migrationRecord
		if readErr := collection.FindOne(ctx, bson.M{"_id": identityBaselineMigrationID}).Decode(&concurrentRecord); readErr != nil {
			return errors.New("identity migration history unavailable")
		}
		return validateMigrationRecord(concurrentRecord)
	}
	return nil
}

func validateMigrationHistory(ctx context.Context, database *mongo.Database) error {
	cursor, err := database.Collection(migrationsCollectionName).Find(ctx, bson.D{})
	if err != nil {
		return errors.New("identity migration history unavailable")
	}
	defer func() { _ = cursor.Close(ctx) }()
	for cursor.Next(ctx) {
		var record migrationRecord
		if err := cursor.Decode(&record); err != nil {
			return errors.New("identity migration history invalid")
		}
		if err := validateMigrationRecord(record); err != nil {
			return err
		}
	}
	if err := cursor.Err(); err != nil {
		return errors.New("identity migration history unavailable")
	}
	return nil
}

func validateMigrationRecord(record migrationRecord) error {
	if record.ID != identityBaselineMigrationID || record.Checksum != identityBaselineMigrationHash || record.AppliedAt.IsZero() {
		return errors.New("identity migration history conflicts with this binary")
	}
	return nil
}

func ensureCollection(ctx context.Context, database *mongo.Database, name string) error {
	if err := database.CreateCollection(ctx, name); err == nil {
		return nil
	}
	names, err := database.ListCollectionNames(ctx, bson.M{"name": name}, options.ListCollections().SetNameOnly(true))
	if err != nil || len(names) != 1 {
		return errors.New("MongoDB collection initialization failed")
	}
	return nil
}

func identityIndexes() []mongo.IndexModel {
	return []mongo.IndexModel{
		{Keys: bson.D{{Key: "credential_hash", Value: 1}}, Options: options.Index().SetName("credential_hash_1").SetUnique(true)},
		{Keys: bson.D{{Key: "credential_id", Value: 1}}, Options: options.Index().SetName("credential_id_1").SetUnique(true)},
		{Keys: bson.D{{Key: "agent_id", Value: 1}}, Options: options.Index().SetName("agent_id_1").SetUnique(true)},
		{Keys: bson.D{{Key: "instance_uid", Value: 1}}, Options: options.Index().SetName("instance_uid_1").SetUnique(true).SetPartialFilterExpression(bson.M{"instance_uid": bson.M{"$type": "string"}})},
	}
}
