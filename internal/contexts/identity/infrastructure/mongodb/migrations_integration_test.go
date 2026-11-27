//go:build integration

package mongodb

import (
	"context"
	"os"
	"reflect"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	driver "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func TestMigrateAndEnsureIndexesHaveSeparateLifecycles(t *testing.T) {
	ctx, database := newMigrationTestDatabase(t)

	if err := Migrate(ctx, database); err != nil {
		t.Fatal("apply identity schema migration", err)
	}
	assertMigrationApplied(t, ctx, database)
	assertIndexNames(t, ctx, database.Collection(collectionName), []string{"_id_"})
	assertIndexNames(t, ctx, database.Collection(invitationsCollectionName), []string{"_id_"})
	if _, err := NewVerifier(database); err != nil {
		t.Fatal("construct verifier without schema side effects", err)
	}
	assertIndexNames(t, ctx, database.Collection(collectionName), []string{"_id_"})

	if err := EnsureIndexes(ctx, database); err != nil {
		t.Fatal("reconcile identity indexes", err)
	}
	assertIndexNames(t, ctx, database.Collection(collectionName), []string{
		"_id_", "credential_hash_1", "credential_id_1", "agent_id_1_scope_active_1", "instance_uid_active_1", "node_id_active_1",
	})
	assertIdentityIndexOptions(t, ctx, database.Collection(collectionName))
	assertIndexNames(t, ctx, database.Collection(invitationsCollectionName), []string{
		"_id_", "core_principal_id_1_idempotency_hash_1", "invitation_id_1", "operation_id_1", "token_hash_1",
	})
	assertIndexNames(t, ctx, database.Collection(invitationEscrowName), []string{"_id_", "expires_at_1"})
	assertIndexNames(t, ctx, database.Collection(attemptsCollectionName), []string{
		"_id_", "agent_id_history_1", "attempt_id_1", "expires_at_1",
	})
	if err := database.Collection(collectionName).Indexes().DropOne(ctx, "agent_id_1_scope_active_1"); err != nil {
		t.Fatal("remove index to verify startup reconciliation", err)
	}

	if err := Migrate(ctx, database); err != nil {
		t.Fatal("reapply identity migration", err)
	}
	assertIndexNames(t, ctx, database.Collection(collectionName), []string{
		"_id_", "credential_hash_1", "credential_id_1", "instance_uid_active_1", "node_id_active_1",
	})
	if err := EnsureIndexes(ctx, database); err != nil {
		t.Fatal("reconcile identity indexes on subsequent startup", err)
	}
	assertIndexNames(t, ctx, database.Collection(collectionName), []string{
		"_id_", "credential_hash_1", "credential_id_1", "agent_id_1_scope_active_1", "instance_uid_active_1", "node_id_active_1",
	})
}

func TestConcurrentMigrateCallsConverge(t *testing.T) {
	ctx, database := newMigrationTestDatabase(t)
	const attempts = 4
	var wait sync.WaitGroup
	errs := make(chan error, attempts)
	for range attempts {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errs <- Migrate(ctx, database)
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent migration failed: %v", err)
		}
	}
	assertMigrationApplied(t, ctx, database)
}

func TestEnsureIndexesReplacesLegacyAgentUniqueness(t *testing.T) {
	ctx, database := newMigrationTestDatabase(t)
	if err := Migrate(ctx, database); err != nil {
		t.Fatal(err)
	}
	collection := database.Collection(collectionName)
	if _, err := collection.Indexes().CreateOne(ctx, driver.IndexModel{
		Keys: bson.D{{Key: "agent_id", Value: 1}}, Options: options.Index().SetName("agent_id_1").SetUnique(true),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := collection.Indexes().CreateMany(ctx, []driver.IndexModel{
		{Keys: bson.D{{Key: "agent_id", Value: 1}, {Key: "scope", Value: 1}}, Options: options.Index().SetName("agent_id_1_scope_1").SetUnique(true)},
		{Keys: bson.D{{Key: "node_id", Value: 1}}, Options: options.Index().SetName("node_id_1").SetUnique(true).SetPartialFilterExpression(bson.M{"node_id": bson.M{"$type": "string"}})},
		{Keys: bson.D{{Key: "instance_uid", Value: 1}}, Options: options.Index().SetName("instance_uid_1").SetUnique(true).SetPartialFilterExpression(bson.M{"instance_uid": bson.M{"$type": "string"}})},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := collection.InsertOne(ctx, bson.M{"agent_id": "legacy-agent", "credential_id": "legacy-opamp", "credential_hash": "legacy-hash", "status": "ACTIVE"}); err != nil {
		t.Fatal(err)
	}
	attempts := database.Collection(attemptsCollectionName)
	if _, err := attempts.Indexes().CreateOne(ctx, driver.IndexModel{
		Keys: bson.D{{Key: "agent_id", Value: 1}}, Options: options.Index().SetName("agent_id_1").SetUnique(true),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := attempts.InsertOne(ctx, bson.M{"attempt_id": "legacy-attempt", "agent_id": "legacy-agent"}); err != nil {
		t.Fatal(err)
	}
	if err := EnsureIndexes(ctx, database); err != nil {
		t.Fatal(err)
	}
	assertIndexNames(t, ctx, collection, []string{"_id_", "credential_hash_1", "credential_id_1", "agent_id_1_scope_active_1", "instance_uid_active_1", "node_id_active_1"})
	var legacy record
	if err := collection.FindOne(ctx, bson.M{"agent_id": "legacy-agent"}).Decode(&legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.Scope != "opamp.connect" {
		t.Fatalf("legacy scope = %q", legacy.Scope)
	}
	if _, err := attempts.InsertOne(ctx, bson.M{"attempt_id": "replacement-attempt", "agent_id": "legacy-agent"}); err != nil {
		t.Fatalf("second historical attempt rejected: %v", err)
	}
	if _, err := collection.InsertOne(ctx, bson.M{"agent_id": "legacy-agent", "credential_id": "new-otlp", "credential_hash": "new-otlp-hash", "scope": "otlp.write", "status": "ACTIVE"}); err != nil {
		t.Fatalf("second scoped credential rejected: %v", err)
	}
	if _, err := collection.InsertOne(ctx, bson.M{"agent_id": "legacy-agent", "credential_id": "duplicate-opamp", "credential_hash": "duplicate-opamp-hash", "scope": "opamp.connect", "status": "ACTIVE"}); !driver.IsDuplicateKeyError(err) {
		t.Fatalf("duplicate OpAMP scope error=%v", err)
	}
	if _, err := collection.InsertOne(ctx, bson.M{"agent_id": "legacy-agent", "credential_id": "revoked-opamp", "credential_hash": "revoked-opamp-hash", "scope": "opamp.connect", "status": "REVOKED"}); err != nil {
		t.Fatalf("revoked OpAMP history should not block a replacement: %v", err)
	}
}

func TestEnsureIndexesFailsClosedOnConflictingIndex(t *testing.T) {
	ctx, database := newMigrationTestDatabase(t)
	if err := Migrate(ctx, database); err != nil {
		t.Fatal("apply schema migration independently of indexes", err)
	}
	collection := database.Collection(collectionName)
	_, err := collection.Indexes().CreateOne(ctx, driver.IndexModel{
		Keys:    bson.D{{Key: "credential_hash", Value: 1}},
		Options: options.Index().SetName("credential_hash_1"),
	})
	if err != nil {
		t.Fatal("create conflicting legacy index", err)
	}

	if err := EnsureIndexes(ctx, database); err == nil {
		t.Fatal("index reconciliation must fail when a required unique index conflicts")
	}
}

func TestEnsureIndexesFailsClosedOnDuplicateLegacyValues(t *testing.T) {
	ctx, database := newMigrationTestDatabase(t)
	if err := Migrate(ctx, database); err != nil {
		t.Fatal("apply schema migration independently of indexes", err)
	}
	collection := database.Collection(collectionName)
	if _, err := collection.InsertMany(ctx, []any{
		bson.M{"credential_hash": "duplicate"},
		bson.M{"credential_hash": "duplicate"},
	}); err != nil {
		t.Fatal("seed duplicate legacy values", err)
	}
	if err := EnsureIndexes(ctx, database); err == nil {
		t.Fatal("index reconciliation must fail when legacy values violate uniqueness")
	}
	var count int64
	count, err := collection.CountDocuments(ctx, bson.M{"credential_hash": "duplicate"})
	if err != nil {
		t.Fatal("count legacy values after failed reconciliation", err)
	}
	if count != 2 {
		t.Fatalf("index reconciliation changed legacy records: count = %d, want 2", count)
	}
}

func TestEnsureIndexesKeepsLegacyIdempotencyIndexWhenCoreScopedDataConflicts(t *testing.T) {
	ctx, database := newMigrationTestDatabase(t)
	if err := Migrate(ctx, database); err != nil {
		t.Fatal("apply schema migration", err)
	}
	collection := database.Collection(invitationsCollectionName)
	legacyIndex := "actor_id_1_idempotency_hash_1"
	if _, err := collection.Indexes().CreateOne(ctx, driver.IndexModel{
		Keys:    bson.D{{Key: "actor_id", Value: 1}, {Key: "idempotency_hash", Value: 1}},
		Options: options.Index().SetName(legacyIndex).SetUnique(true),
	}); err != nil {
		t.Fatal("create legacy actor-scoped idempotency index", err)
	}
	if _, err := collection.InsertMany(ctx, []any{
		bson.M{"_id": "legacy-a", "operation_id": "legacy-op-a", "core_principal_id": "core-service", "idempotency_hash": "same-key-hash", "actor_id": "human-a"},
		bson.M{"_id": "legacy-b", "operation_id": "legacy-op-b", "core_principal_id": "core-service", "idempotency_hash": "same-key-hash", "actor_id": "human-b"},
	}); err != nil {
		t.Fatal("seed data that conflicts with Core-scoped uniqueness but satisfies legacy index", err)
	}
	if err := EnsureIndexes(ctx, database); err == nil {
		t.Fatal("index reconciliation must fail closed on conflicting Core-scoped idempotency data")
	}
	if !indexExists(t, ctx, collection, legacyIndex) {
		t.Fatal("failed Core-scoped index creation must retain the legacy index")
	}
}

func TestMigrateRejectsChangedAppliedMigration(t *testing.T) {
	ctx, database := newMigrationTestDatabase(t)
	if err := Migrate(ctx, database); err != nil {
		t.Fatal("apply identity schema migration", err)
	}
	_, err := database.Collection(migrationsCollectionName).UpdateOne(ctx,
		bson.M{"_id": identityBaselineMigrationID}, bson.M{"$set": bson.M{"checksum": "changed"}},
	)
	if err != nil {
		t.Fatal("corrupt applied migration marker for test", err)
	}
	if err := Migrate(ctx, database); err == nil {
		t.Fatal("migration must fail closed when an applied version checksum changes")
	}
}

func newMigrationTestDatabase(t *testing.T) (context.Context, *driver.Database) {
	t.Helper()
	uri := os.Getenv("FLEET_MONGODB_URI")
	if uri == "" {
		uri = os.Getenv("MONGODB_URI")
	}
	if uri == "" {
		requireIntegrationDependency(t, "set FLEET_MONGODB_URI or MONGODB_URI to run Mongo migration integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	client, err := driver.Connect(options.Client().ApplyURI(uri).SetTimeout(5 * time.Second))
	if err != nil {
		t.Fatal("connect MongoDB", err)
	}
	databaseName := "fleet_identity_migrations_test_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	database := client.Database(databaseName)
	t.Logf("Mongo integration database: %s (dropped by test cleanup)", databaseName)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if err := database.Drop(cleanupCtx); err != nil {
			t.Errorf("drop migration test database: %v", err)
		}
		if err := client.Disconnect(cleanupCtx); err != nil {
			t.Errorf("disconnect MongoDB test client: %v", err)
		}
	})
	if err := client.Ping(ctx, nil); err != nil {
		t.Fatal("ping MongoDB", err)
	}
	return ctx, database
}

func assertMigrationApplied(t *testing.T, ctx context.Context, database *driver.Database) {
	t.Helper()
	var record struct {
		ID       string    `bson:"_id"`
		Checksum string    `bson:"checksum"`
		Applied  time.Time `bson:"applied_at"`
	}
	if err := database.Collection(migrationsCollectionName).
		FindOne(ctx, bson.M{"_id": identityBaselineMigrationID}).Decode(&record); err != nil {
		t.Fatal("read migration history", err)
	}
	if record.ID != identityBaselineMigrationID || record.Checksum != identityBaselineMigrationHash || record.Applied.IsZero() {
		t.Fatalf("migration history record is incomplete: %+v", record)
	}
	var enrollmentMigration migrationRecord
	if err := database.Collection(migrationsCollectionName).FindOne(ctx, bson.M{"_id": enrollmentMigrationID}).Decode(&enrollmentMigration); err != nil {
		t.Fatal("read enrollment migration history", err)
	}
	if enrollmentMigration.Checksum != enrollmentMigrationHash || enrollmentMigration.AppliedAt.IsZero() {
		t.Fatalf("enrollment migration history record is incomplete: %+v", enrollmentMigration)
	}
}

func assertIdentityIndexOptions(t *testing.T, ctx context.Context, collection *driver.Collection) {
	t.Helper()
	cursor, err := collection.Indexes().List(ctx)
	if err != nil {
		t.Fatal("list identity indexes", err)
	}
	var indexes []bson.D
	if err := cursor.All(ctx, &indexes); err != nil {
		t.Fatal("decode identity index options", err)
	}
	for _, index := range indexes {
		name, _ := documentValue(index, "name").(string)
		if name == "_id_" {
			continue
		}
		if unique, _ := documentValue(index, "unique").(bool); !unique {
			t.Errorf("index %q is not unique", name)
		}
		if name == "instance_uid_active_1" {
			partial, ok := documentValue(index, "partialFilterExpression").(bson.D)
			condition, conditionOK := documentValue(partial, "instance_uid").(bson.D)
			filterType, typeOK := documentValue(condition, "$type").(string)
			if !ok || !conditionOK || !typeOK || filterType != "string" || documentValue(partial, "status") != "ACTIVE" {
				t.Errorf("instance UID index has wrong partial filter: %#v", documentValue(index, "partialFilterExpression"))
			}
		}
		if name == "node_id_active_1" {
			partial, ok := documentValue(index, "partialFilterExpression").(bson.D)
			condition, conditionOK := documentValue(partial, "node_id").(bson.D)
			filterType, typeOK := documentValue(condition, "$type").(string)
			if !ok || !conditionOK || !typeOK || filterType != "string" || documentValue(partial, "status") != "ACTIVE" {
				t.Errorf("Node ID index has wrong partial filter: %#v", documentValue(index, "partialFilterExpression"))
			}
		}
		if name == "agent_id_1_scope_active_1" {
			partial, ok := documentValue(index, "partialFilterExpression").(bson.D)
			if !ok || documentValue(partial, "status") != "ACTIVE" {
				t.Errorf("active credential index has wrong partial filter: %#v", documentValue(index, "partialFilterExpression"))
			}
		}
	}
}

func documentValue(document bson.D, key string) any {
	for _, element := range document {
		if element.Key == key {
			return element.Value
		}
	}
	return nil
}

func assertIndexNames(t *testing.T, ctx context.Context, collection *driver.Collection, want []string) {
	t.Helper()
	cursor, err := collection.Indexes().List(ctx)
	if err != nil {
		t.Fatal("list collection indexes", err)
	}
	var indexes []struct {
		Name string `bson:"name"`
	}
	if err := cursor.All(ctx, &indexes); err != nil {
		t.Fatal("decode collection indexes", err)
	}
	got := make([]string, 0, len(indexes))
	for _, index := range indexes {
		got = append(got, index.Name)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("index names = %v, want %v", got, want)
	}
}

func indexExists(t *testing.T, ctx context.Context, collection *driver.Collection, name string) bool {
	t.Helper()
	cursor, err := collection.Indexes().List(ctx)
	if err != nil {
		t.Fatal("list collection indexes", err)
	}
	var indexes []struct {
		Name string `bson:"name"`
	}
	if err := cursor.All(ctx, &indexes); err != nil {
		t.Fatal("decode collection indexes", err)
	}
	if err := cursor.Close(ctx); err != nil {
		t.Fatal("close collection index cursor", err)
	}
	for _, index := range indexes {
		if index.Name == name {
			return true
		}
	}
	return false
}
