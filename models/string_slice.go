package models

import (
	"database/sql/driver"
	"fmt"
	"strings"
)

// StringSlice is a Go []string that serialises to/from a PostgreSQL text[] literal.
// GORM uses the Value / Scan methods automatically for any column typed text[].
//
// Encoding example:  []string{"a", "b"} → '{"a","b"}'
// Decoding example:  '{"a","b"}' → []string{"a", "b"}
type StringSlice []string

// Value implements driver.Valuer — called when writing to the DB.
func (s StringSlice) Value() (driver.Value, error) {
	if len(s) == 0 {
		return "{}", nil
	}
	parts := make([]string, len(s))
	for i, v := range s {
		v = strings.ReplaceAll(v, `\`, `\\`)
		v = strings.ReplaceAll(v, `"`, `\"`)
		parts[i] = `"` + v + `"`
	}
	return "{" + strings.Join(parts, ",") + "}", nil
}

// Scan implements sql.Scanner — called when reading from the DB.
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
	case []string:
		*s = StringSlice(v)
		return nil
	default:
		return fmt.Errorf("StringSlice: cannot scan type %T", value)
	}

	// Strip surrounding braces
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "{")
	raw = strings.TrimSuffix(raw, "}")

	if raw == "" {
		*s = StringSlice{}
		return nil
	}

	// Simple CSV-style split that respects double-quoted elements.
	*s = splitPGArray(raw)
	return nil
}

// splitPGArray splits a PostgreSQL array body (without outer braces) into elements.
// It handles quoted elements that may contain commas.
func splitPGArray(s string) []string {
	var elems []string
	var cur strings.Builder
	inQuote := false

	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == '"' && !inQuote:
			inQuote = true
		case ch == '"' && inQuote:
			// peek for escaped quote
			if i+1 < len(s) && s[i+1] == '"' {
				cur.WriteByte('"')
				i++
			} else {
				inQuote = false
			}
		case ch == ',' && !inQuote:
			elems = append(elems, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(ch)
		}
	}
	elems = append(elems, cur.String())
	return elems
}
