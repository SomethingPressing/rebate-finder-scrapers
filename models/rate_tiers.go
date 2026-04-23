package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// RateTier is one incentive tier within a tiered program (e.g. HV101a — Split AC < 5.4 Tons).
type RateTier struct {
	ID          string  `json:"id"`
	Description string  `json:"description"`
	Amount      float64 `json:"amount"`
	Unit        string  `json:"unit"`
}

// RateTiersJSON is a GORM-compatible JSONB type for []RateTier.
// It serialises to/from a PostgreSQL jsonb column automatically.
type RateTiersJSON []RateTier

// Value implements driver.Valuer — marshals to JSON when writing to the DB.
func (r RateTiersJSON) Value() (driver.Value, error) {
	if r == nil {
		return nil, nil
	}
	b, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// Scan implements sql.Scanner — unmarshals from JSON when reading from the DB.
func (r *RateTiersJSON) Scan(value interface{}) error {
	if value == nil {
		*r = RateTiersJSON{}
		return nil
	}
	var b []byte
	switch v := value.(type) {
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		return fmt.Errorf("RateTiersJSON: cannot scan type %T", value)
	}
	return json.Unmarshal(b, r)
}
