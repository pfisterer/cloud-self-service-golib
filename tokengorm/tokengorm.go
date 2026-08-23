// Package tokengorm persists tokens with GORM, as the token.Store a Service
// needs.
//
// It is a separate package so that token itself stays free of a database
// dependency: a service running its storage in memory should not carry GORM
// into its build graph to get at the credential logic.
//
// The row lives in a table named "tokens", which is where dynamic-zones has
// kept them since before this package existed. Column names are pinned
// explicitly rather than derived from the field names — see Migrate for what
// that is worth.
package tokengorm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/pfisterer/cloud-self-service-golib/token"
	"gorm.io/gorm"
)

// Token is the stored row. It is not the API shape: a service that returns
// tokens over HTTP should map to a type it controls, so that a column rename
// here is not a change to its published API.
type Token struct {
	ID        uint      `gorm:"primaryKey"`
	CreatedAt time.Time

	// Subject is the identity the token acts as. Indexed because listing a
	// person's own tokens is the query the UI makes on every page view.
	Subject string `gorm:"column:subject;index"`

	// Hash is the SHA-256 of the secret, hex encoded. The secret itself is
	// never stored — whoever reads this database (a backup, a pgweb session, a
	// pod on the same network) would otherwise hold working credentials for
	// every user.
	//
	// Unique so that the same token cannot exist twice, which would make a
	// lookup ambiguous rather than merely redundant.
	Hash string `gorm:"column:token_hash;uniqueIndex"`

	// Prefix is the leading, non-secret part, kept in the clear so a person can
	// tell two of their tokens apart without either being shown.
	Prefix string `gorm:"column:token_prefix"`

	ReadOnly bool `gorm:"default:false"`

	// ExpiresAt is the zero time for a token that does not expire.
	ExpiresAt time.Time
}

// TableName pins the table so that renaming the Go type cannot silently point
// at a different one.
func (Token) TableName() string { return "tokens" }

func (t Token) toRecord() token.Record {
	return token.Record{
		ID:        t.ID,
		Subject:   t.Subject,
		Hash:      t.Hash,
		Prefix:    t.Prefix,
		ReadOnly:  t.ReadOnly,
		CreatedAt: t.CreatedAt,
		ExpiresAt: t.ExpiresAt,
	}
}

func fromRecord(rec token.Record) Token {
	return Token{
		ID:        rec.ID,
		Subject:   rec.Subject,
		Hash:      rec.Hash,
		Prefix:    rec.Prefix,
		ReadOnly:  rec.ReadOnly,
		CreatedAt: rec.CreatedAt,
		ExpiresAt: rec.ExpiresAt,
	}
}

// Migrate brings the tokens table up to date, including the one rename this
// package inherited.
//
// The rename has to happen before AutoMigrate, and this is the reason the
// package exists rather than a bare model: AutoMigrate does not rename columns.
// Run against a database that still has "username", it would add an empty
// "subject" beside it and leave every existing token pointing at nobody — a
// lookup that finds nothing, with no error anywhere. For dynamic-zones that
// would have quietly invalidated every API token in the field, most of them
// sitting in home routers where nobody would connect the two events.
//
// Guarded on both sides so it is idempotent, and a no-op for a fresh database.
func Migrate(db *gorm.DB) error {
	m := db.Migrator()

	if m.HasTable(&Token{}) {
		if m.HasColumn(&Token{}, "username") && !m.HasColumn(&Token{}, "subject") {
			if err := m.RenameColumn(&Token{}, "username", "subject"); err != nil {
				return fmt.Errorf("tokengorm.Migrate: renaming username to subject: %w", err)
			}
		}

		// Renaming a column leaves its indexes behind under their old names, and
		// AutoMigrate then adds the ones it expects — so the table would end up
		// with two indexes on subject and two unique indexes on token_hash, one
		// pair of them named after a column that no longer exists. Harmless to
		// query, permanent to look at, and a second migration to remove later.
		//
		// Dropped rather than renamed, and by raw SQL rather than through the
		// migrator: GORM's postgres migrator emits invalid SQL for both
		// RenameIndex and DropIndex here ("syntax error at or near
		// CURRENT_SCHEMA"). Plain DROP INDEX IF EXISTS is understood by both
		// drivers that occur in practice, and AutoMigrate recreates the index
		// below under the name it expects. The gap between the two is inside
		// startup, before the server accepts a request.
		//
		// The names are constants from this file, so there is nothing here to
		// interpolate unsafely. Dialects other than these two keep their stale
		// index — no worse than not cleaning up at all.
		//
		// GORM derives index names from the FIELD, not the column, which is why
		// the new hash index is idx_tokens_hash and not idx_tokens_token_hash.
		switch db.Dialector.Name() {
		case "postgres", "sqlite":
			// Unconditional, not "only if the replacement is missing": these
			// names belong to a schema this package never produces, so their
			// presence is always leftover. A guard on the replacement would
			// leave a database that already has both — one that ran an earlier
			// version of this function — stuck with the duplicate forever.
			for _, stale := range []string{"idx_tokens_username", "idx_tokens_token_hash"} {
				if !m.HasIndex(&Token{}, stale) {
					continue
				}
				if err := db.Exec("DROP INDEX IF EXISTS " + stale).Error; err != nil {
					return fmt.Errorf("tokengorm.Migrate: dropping stale index %s: %w", stale, err)
				}
			}
		}
	}

	if err := db.AutoMigrate(&Token{}); err != nil {
		return fmt.Errorf("tokengorm.Migrate: %w", err)
	}
	return nil
}

// NewService migrates the tokens table and returns a token.Service on it.
//
// The one call both services want, and the reason it exists: NewStore alone
// gives a Service that compiles, starts and fails at runtime if Migrate was
// forgotten. Nothing about the type says the migration is required, so the
// constructor does it.
func NewService(prefix string, db *gorm.DB) (*token.Service, error) {
	if err := Migrate(db); err != nil {
		return nil, err
	}
	return token.NewService(prefix, NewStore(db)), nil
}

// Store is a token.Store backed by GORM.
type Store struct {
	db *gorm.DB
}

// NewStore returns a Store on the given connection. Prefer NewService, which
// cannot be used without migrating first.
func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

func (s *Store) Insert(ctx context.Context, rec token.Record) (token.Record, error) {
	row := fromRecord(rec)
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		// Neither the secret nor the hash goes into the error: this is logged.
		return token.Record{}, fmt.Errorf("tokengorm.Insert: %w", err)
	}
	return row.toRecord(), nil
}

func (s *Store) ByHash(ctx context.Context, hash string) (token.Record, error) {
	var row Token
	err := s.db.WithContext(ctx).Where("token_hash = ?", hash).Take(&row).Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return token.Record{}, token.ErrNotFound
	}
	if err != nil {
		return token.Record{}, fmt.Errorf("tokengorm.ByHash: %w", err)
	}
	return row.toRecord(), nil
}

func (s *Store) BySubject(ctx context.Context, subject string) ([]token.Record, error) {
	var rows []Token
	if err := s.db.WithContext(ctx).
		Where("subject = ?", subject).
		Order("id").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("tokengorm.BySubject: %w", err)
	}

	out := make([]token.Record, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toRecord())
	}
	return out, nil
}

func (s *Store) Delete(ctx context.Context, subject string, id uint) error {
	// Both conditions, always: matching on the ID alone would let anyone guess
	// a number and revoke somebody else's credential.
	result := s.db.WithContext(ctx).
		Where("subject = ? AND id = ?", subject, id).
		Delete(&Token{})

	if result.Error != nil {
		return fmt.Errorf("tokengorm.Delete: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return token.ErrNotFound
	}
	return nil
}

func (s *Store) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	// The zero time means "never expires", so it must not be swept: without the
	// first condition every permanent token would be deleted on the first run.
	result := s.db.WithContext(ctx).
		Where("expires_at IS NOT NULL AND expires_at != ? AND expires_at <= ?", time.Time{}, before).
		Delete(&Token{})

	if result.Error != nil {
		return 0, fmt.Errorf("tokengorm.DeleteExpired: %w", result.Error)
	}
	return result.RowsAffected, nil
}
