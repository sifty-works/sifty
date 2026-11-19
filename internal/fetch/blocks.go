package fetch

import (
	"time"

	"github.com/sifty-works/sifty-server/internal/db"
)

type DomainBlocks struct {
	db *db.DB
}

func NewDomainBlocks(database *db.DB) *DomainBlocks {
	return &DomainBlocks{db: database}
}

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

func (s *DomainBlocks) Block(domain string, until time.Time) {
	s.db.Exec(
		`INSERT INTO domain_block (domain, until) VALUES (?, ?)
         ON CONFLICT(domain) DO UPDATE SET until = excluded.until`,
		domain, until.UTC().Format(time.RFC3339),
	)
}
