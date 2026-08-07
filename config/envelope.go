package config

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"gorm.io/gorm"
)

// The demand envelope — the compiled, tenant-blind shopping list that the
// broker publishes and collectors work from (v0.8 Feature 4).
//
// This is what replaces reading tenants.json to decide what to collect. The
// decision recorded on 2026-08-07 was that multi-source collectors are fine
// PROVIDED they fetch their source configuration rather than carry it baked
// in — this file is that fetch.
//
// A collector reads the envelope from the shared staging database, which it
// already has credentials for. There is no inbound path and no new secret.
// Crucially the envelope contains no tenant identities: a collector cannot
// tell whether one tenant or ten asked for a ZIP code, which is what keeps
// the fleet tenant-blind.

// SourceDemand is what everybody, added together, wants from one source.
type SourceDemand struct {
	States       []string `json:"states"`
	Utilities    []string `json:"utilities"`
	ServiceAreas []string `json:"service_areas"`
	ZipCodes     []string `json:"zip_codes"`
	// Unbounded means at least one subscriber asked for this source with no
	// territory filter at all. The lists are then advisory rather than
	// limiting, and a collector must not treat them as a boundary.
	Unbounded bool `json:"unbounded"`
	// Subscribers is a count, never a list of identities.
	Subscribers int `json:"subscribers"`
}

// DemandEnvelope is one published version of the fleet's collection targets.
type DemandEnvelope struct {
	Version int64                   `json:"-"`
	Sources map[string]SourceDemand `json:"sources"`
	// UnsubscribedSources are sources nobody currently wants. A collector
	// covering one of these can stand down.
	UnsubscribedSources []string `json:"unsubscribedSources"`
}

// SourceNames returns the sources with at least one subscriber, sorted.
func (e *DemandEnvelope) SourceNames() []string {
	if e == nil {
		return nil
	}
	names := make([]string, 0, len(e.Sources))
	for name := range e.Sources {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Wants reports whether anybody is asking for this source.
func (e *DemandEnvelope) Wants(source string) bool {
	if e == nil {
		return true // no envelope published: collect as before
	}
	_, ok := e.Sources[source]
	return ok
}

// BrokerSchema is where the broker's own tables live. Overridable so a
// deployment can rename the schema without a code change.
func BrokerSchema() string {
	if s := os.Getenv("BROKER_SCHEMA"); s != "" {
		return s
	}
	return "broker"
}

// LoadEnvelope reads the newest published envelope.
//
// Returns (nil, nil) when there is no envelope to read — either the broker has
// never published one or its schema does not exist yet. That is a normal state
// during the transition and callers must fall back to their previous source
// selection rather than collecting nothing.
func LoadEnvelope(db *gorm.DB) (*DemandEnvelope, error) {
	if db == nil {
		return nil, nil
	}

	var row struct {
		Version int64  `gorm:"column:version"`
		Payload []byte `gorm:"column:payload"`
	}
	query := fmt.Sprintf(
		"SELECT version, payload FROM %s.demand_envelopes ORDER BY version DESC LIMIT 1",
		BrokerSchema(),
	)
	if err := db.Raw(query).Scan(&row).Error; err != nil {
		// A missing table is not an error worth failing collection over.
		return nil, nil
	}
	if len(row.Payload) == 0 {
		return nil, nil
	}

	var envelope DemandEnvelope
	if err := json.Unmarshal(row.Payload, &envelope); err != nil {
		return nil, fmt.Errorf("parse demand envelope v%d: %w", row.Version, err)
	}
	envelope.Version = row.Version
	return &envelope, nil
}
