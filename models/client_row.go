package models

// ClientRow holds the fields the promoter writes to public.clients.
// Used by db.EnsureClient to upsert the tenant's identity row before promoting rebates.
type ClientRow struct {
	ID          string
	Name        string
	Region      string
	UtilityType string
	IsDemo      bool
}

// IsSet returns true when the row has a usable ID and name — the minimum
// required to write a meaningful clients row.
func (c ClientRow) IsSet() bool { return c.ID != "" && c.Name != "" }
