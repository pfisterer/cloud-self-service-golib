package tokengorm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pfisterer/cloud-self-service-golib/token"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	t.Cleanup(func() {
		sql, err := db.DB()
		if err == nil {
			_ = sql.Close()
		}
	})
	return db
}

func migratedStore(t *testing.T) (*Store, *gorm.DB) {
	t.Helper()
	db := newDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return NewStore(db), db
}

// The test this package exists for. AutoMigrate does not rename columns: run
// against the old schema it would add an empty "subject" beside "username" and
// leave every token owned by nobody — no error, just a lookup that stops
// finding anything, for credentials that live in home routers.
func TestMigrateRenamesUsernameKeepingTheValues(t *testing.T) {
	ctx := context.Background()
	db := newDB(t)

	// The shape dynamic-zones has on disk today.
	type legacyToken struct {
		ID          uint `gorm:"primaryKey"`
		CreatedAt   time.Time
		Username    string `gorm:"index"`
		TokenHash   string `gorm:"uniqueIndex"`
		TokenPrefix string
		ReadOnly    bool
		ExpiresAt   time.Time
	}
	if err := db.Table("tokens").AutoMigrate(&legacyToken{}); err != nil {
		t.Fatalf("creating the legacy table: %v", err)
	}

	expires := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	if err := db.Table("tokens").Create(&legacyToken{
		Username:    "alice@example.edu",
		TokenHash:   token.Hash("dynz_token_deadbeef"),
		TokenPrefix: "dynz_token_deadbeef"[:19],
		ReadOnly:    true,
		ExpiresAt:   expires,
	}).Error; err != nil {
		t.Fatalf("seeding a token: %v", err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if db.Migrator().HasColumn(&Token{}, "username") {
		t.Error("the old column is still there")
	}

	// The point: the existing token still works, for the same person.
	rec, err := NewStore(db).ByHash(ctx, token.Hash("dynz_token_deadbeef"))
	if err != nil {
		t.Fatalf("the migrated token cannot be looked up: %v", err)
	}
	if rec.Subject != "alice@example.edu" {
		t.Errorf("Subject = %q, want it carried over from username", rec.Subject)
	}
	if !rec.ReadOnly {
		t.Error("ReadOnly was lost")
	}
	if !rec.ExpiresAt.UTC().Truncate(time.Second).Equal(expires) {
		t.Errorf("ExpiresAt = %v, want %v", rec.ExpiresAt, expires)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	db := newDB(t)
	for i := 0; i < 3; i++ {
		if err := Migrate(db); err != nil {
			t.Fatalf("Migrate run %d: %v", i+1, err)
		}
	}
	if !db.Migrator().HasColumn(&Token{}, "subject") {
		t.Error("subject column missing")
	}
}

func TestStoreRoundtrip(t *testing.T) {
	ctx := context.Background()
	store, _ := migratedStore(t)
	svc := token.NewService("dynz_token_", store)

	issued, err := svc.Issue(ctx, "alice@example.edu", time.Hour, false)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if issued.ID == 0 {
		t.Error("the store did not assign an ID")
	}

	rec, err := svc.Lookup(ctx, issued.Secret)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if rec.Subject != "alice@example.edu" || rec.ID != issued.ID {
		t.Errorf("got %+v", rec)
	}

	list, err := svc.List(ctx, "alice@example.edu")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d tokens, want 1", len(list))
	}

	if err := svc.Revoke(ctx, "alice@example.edu", issued.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := svc.Lookup(ctx, issued.Secret); !errors.Is(err, token.ErrNotFound) {
		t.Errorf("after revoking: err = %v, want ErrNotFound", err)
	}
}

// The secret must not be reachable from the database, by any column.
func TestSecretIsNotStored(t *testing.T) {
	ctx := context.Background()
	store, db := migratedStore(t)
	svc := token.NewService("dynz_token_", store)

	issued, err := svc.Issue(ctx, "alice@example.edu", time.Hour, false)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	var rows []map[string]any
	if err := db.Table("tokens").Find(&rows).Error; err != nil {
		t.Fatalf("reading the table: %v", err)
	}
	for _, row := range rows {
		for column, value := range row {
			if s, ok := value.(string); ok && s == issued.Secret {
				t.Fatalf("column %q holds the secret in the clear", column)
			}
		}
	}
}

func TestDeleteIsScopedToTheOwner(t *testing.T) {
	ctx := context.Background()
	store, _ := migratedStore(t)
	svc := token.NewService("dynz_token_", store)

	issued, err := svc.Issue(ctx, "alice@example.edu", time.Hour, false)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	if err := store.Delete(ctx, "mallory@example.edu", issued.ID); !errors.Is(err, token.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if _, err := svc.Lookup(ctx, issued.Secret); err != nil {
		t.Error("a stranger revoked the token")
	}
}

// A permanent token has the zero expiry, which must not read as "expired long
// ago" to the sweep — otherwise the first cleanup run deletes every service
// credential on the platform.
func TestDeleteExpiredSparesPermanentTokens(t *testing.T) {
	ctx := context.Background()
	store, _ := migratedStore(t)
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	svc := token.NewService("dynz_token_", store).WithClock(func() time.Time { return now })

	permanent, err := svc.Issue(ctx, "service@example.edu", token.NeverExpires, false)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	shortLived, err := svc.Issue(ctx, "alice@example.edu", time.Hour, false)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	later := svc.WithClock(func() time.Time { return now.Add(2 * time.Hour) })
	n, err := later.DeleteExpired(ctx)
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if n != 1 {
		t.Errorf("deleted %d rows, want 1", n)
	}

	if _, err := later.Lookup(ctx, permanent.Secret); err != nil {
		t.Errorf("the permanent token was swept: %v", err)
	}
	if _, err := later.Lookup(ctx, shortLived.Secret); !errors.Is(err, token.ErrNotFound) {
		t.Error("the expired token survived")
	}
}

func TestByHashUnknown(t *testing.T) {
	ctx := context.Background()
	store, _ := migratedStore(t)

	if _, err := store.ByHash(ctx, token.Hash("nope")); !errors.Is(err, token.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
