package db

import (
	"net/url"
	"testing"
)

func TestSanitizePostgresDSN_stripsPrismaSchema(t *testing.T) {
	in := "postgresql://postgres:secret@localhost:5432/rebatefinder.dev?schema=public"
	got := sanitizePostgresDSN(in)
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("schema") != "" {
		t.Fatalf("schema param should be removed, got %q", got)
	}
	if u.Path != "/rebatefinder.dev" {
		t.Fatalf("path: %q", u.Path)
	}
}

func TestSanitizePostgresDSN_preservesOtherParams(t *testing.T) {
	in := "postgresql://u:p@h:5432/db?schema=public&sslmode=disable"
	got := sanitizePostgresDSN(in)
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("sslmode") != "disable" {
		t.Fatalf("got %q", got)
	}
}

func TestSanitizePostgresDSN_unchangedWithoutSchema(t *testing.T) {
	in := "postgresql://u:p@h:5432/db"
	if got := sanitizePostgresDSN(in); got != in {
		t.Fatalf("got %q want %q", got, in)
	}
}
