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
	if _, err := NewVerifier(database); err != nil {
		t.Fatal("construct verifier without schema side effects", err)
	}
	assertIndexNames(t, ctx, database.Collection(collectionName), []string{"_id_"})

	if err := EnsureIndexes(ctx, database); err != nil {
		t.Fatal("reconcile identity indexes", err)
	}
	assertIndexNames(t, ctx, database.Collection(collectionName), []string{
		"_id_", "credential_hash_1", "credential_id_1", "agent_id_1", "instance_uid_1",
	})
	assertIdentityIndexOptions(t, ctx, database.Collection(collectionName))
	if err := database.Collection(collectionName).Indexes().DropOne(ctx, "agent_id_1"); err != nil {
		t.Fatal("remove index to verify startup reconciliation", err)
	}

	if err := Migrate(ctx, database); err != nil {
		t.Fatal("reapply identity migration", err)
	}
	assertIndexNames(t, ctx, database.Collection(collectionName), []string{
		"_id_", "credential_hash_1", "credential_id_1", "instance_uid_1",
	})
	if err := EnsureIndexes(ctx, database); err != nil {
		t.Fatal("reconcile identity indexes on subsequent startup", err)
	}
	assertIndexNames(t, ctx, database.Collection(collectionName), []string{
		"_id_", "credential_hash_1", "credential_id_1", "agent_id_1", "instance_uid_1",
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
	database := client.Database("fleet_identity_migrations_test_" + strconv.FormatInt(time.Now().UnixNano(), 10))
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
		if name == "instance_uid_1" {
			partial, ok := documentValue(index, "partialFilterExpression").(bson.D)
			condition, conditionOK := documentValue(partial, "instance_uid").(bson.D)
			filterType, typeOK := documentValue(condition, "$type").(string)
			if !ok || !conditionOK || !typeOK || filterType != "string" {
				t.Errorf("instance UID index has wrong partial filter: %#v", documentValue(index, "partialFilterExpression"))
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
