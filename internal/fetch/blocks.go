package fetch

import (
	"time"

	"github.com/sifty-works/sifty-server/internal/db"
)

// DomainBlocks is a persistent block store backed by the SQLite database.
// A failed read is treated as not blocked; a failed write is ignored.
type DomainBlocks struct {
	db *db.DB
}

// NewDomainBlocks returns a DomainBlocks backed by database.
func NewDomainBlocks(database *db.DB) *DomainBlocks {
	return &DomainBlocks{db: database}
}

// Blocked reports whether the domain is currently blocked and until when.
func (s *DomainBlocks) Blocked(domain string) (time.Time, bool) {
	var until string
	if err := s.db.QueryRow(
		`SELECT until FROM domain_block WHERE domain = ?`, domain,
	).Scan(&until); err != nil {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, until)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// Block records that domain is blocked until the given time.
func (s *DomainBlocks) Block(domain string, until time.Time) {
	s.db.Exec( //nolint:errcheck
		`INSERT INTO domain_block (domain, until) VALUES (?, ?)
         ON CONFLICT(domain) DO UPDATE SET until = excluded.until`,
		domain, until.UTC().Format(time.RFC3339),
	)
}
