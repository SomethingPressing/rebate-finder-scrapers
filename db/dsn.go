package db

import (
	"net/url"
	"strings"
)

// sanitizePostgresDSN removes query parameters that Prisma adds but PostgreSQL
// rejects as startup options (e.g. ?schema=public). The scraper shares
// DATABASE_URL with the Next.js app; strip Prisma-only keys for GORM/pg.
func sanitizePostgresDSN(dsn string) string {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return dsn
	}

	u, err := url.Parse(dsn)
	if err != nil || u.Scheme == "" {
		return dsn
	}

	switch strings.ToLower(u.Scheme) {
	case "postgres", "postgresql":
	default:
		return dsn
	}

	q := u.Query()
	if q.Get("schema") == "" {
		return dsn
	}

	q.Del("schema")
	u.RawQuery = q.Encode()
	return u.String()
}
