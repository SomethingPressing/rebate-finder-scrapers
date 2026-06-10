package db

import (
	"database/sql/driver"
	"fmt"
	"strings"
)

// StringSlice is a []string that knows how to read and write the PostgreSQL
// native text-array wire format: {val1,"val 2","val,3"}.
//
// Used for scraper_source_configs columns (states, utilities, service_areas,
// zip_codes) so that GORM + pgx can round-trip them without lib/pq.
type StringSlice []string

// Scan implements sql.Scanner.
// The driver delivers the value as a string in PostgreSQL array literal syntax.
func (s *StringSlice) Scan(value interface{}) error {
	if value == nil {
		*s = StringSlice{}
		return nil
	}

	var raw string
	switch v := value.(type) {
	case string:
		raw = v
	case []byte:
		raw = string(v)
	default:
		return fmt.Errorf("StringSlice.Scan: unsupported source type %T", value)
	}

	*s = parsePostgresArray(raw)
	return nil
}

// Value implements driver.Valuer.
func (s StringSlice) Value() (driver.Value, error) {
	if s == nil {
		return "{}", nil
	}
	parts := make([]string, len(s))
	for i, v := range s {
		// Quote values that contain special characters.
		if strings.ContainsAny(v, `{},"\`) || strings.ContainsRune(v, ' ') {
			v = `"` + strings.ReplaceAll(v, `"`, `\"`) + `"`
		}
		parts[i] = v
	}
	return "{" + strings.Join(parts, ",") + "}", nil
}

// parsePostgresArray parses a PostgreSQL array literal into a []string.
// Handles: {}, {a,b,c}, {"a b","c,d"}, {NULL}.
func parsePostgresArray(s string) []string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") {
		if s == "" {
			return []string{}
		}
		return []string{s}
	}
	inner := s[1 : len(s)-1]
	if inner == "" {
		return []string{}
	}

	var result []string
	var cur strings.Builder
	inQuotes := false
	i := 0
	for i < len(inner) {
		ch := inner[i]
		switch {
		case ch == '"' && !inQuotes:
			inQuotes = true
		case ch == '"' && inQuotes:
			// Check for escaped quote: \"
			if i+1 < len(inner) && inner[i+1] == '"' {
				cur.WriteByte('"')
				i++ // skip second quote
			} else {
				inQuotes = false
			}
		case ch == '\\' && inQuotes && i+1 < len(inner):
			i++
			cur.WriteByte(inner[i])
		case ch == ',' && !inQuotes:
			result = append(result, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(ch)
		}
		i++
	}
	result = append(result, cur.String())
	return result
}
