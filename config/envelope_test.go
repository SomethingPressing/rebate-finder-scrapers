package config

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The envelope is what decides whether a collector runs at all, so these tests
// pin the two things that would hurt most if they broke: collecting nothing
// when the envelope is missing, and leaking a tenant identity into the fleet.

func TestEnvelopeSourceNamesSorted(t *testing.T) {
	e := &DemandEnvelope{Sources: map[string]SourceDemand{
		"rewiring_america": {Subscribers: 1},
		"dsireusa":         {Subscribers: 2},
		"energy_star":      {Subscribers: 1},
	}}
	got := e.SourceNames()
	want := []string{"dsireusa", "energy_star", "rewiring_america"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SourceNames() = %v, want %v", got, want)
	}
}

func TestNilEnvelopeCollectsAsBefore(t *testing.T) {
	// A collector with no published envelope must keep its previous behaviour
	// rather than standing down entirely — otherwise publishing the first
	// envelope late would silently stop all collection.
	var e *DemandEnvelope
	if !e.Wants("dsireusa") {
		t.Fatal("a nil envelope must not suppress collection")
	}
	if e.SourceNames() != nil {
		t.Fatal("a nil envelope has no source names")
	}
}

func TestWantsOnlySubscribedSources(t *testing.T) {
	e := &DemandEnvelope{
		Sources:             map[string]SourceDemand{"dsireusa": {Subscribers: 1}},
		UnsubscribedSources: []string{"srp"},
	}
	if !e.Wants("dsireusa") {
		t.Error("subscribed source should be wanted")
	}
	if e.Wants("srp") {
		t.Error("unsubscribed source must not be collected — that is the point of the envelope")
	}
	if e.Wants("never_heard_of_it") {
		t.Error("unknown source must not be collected")
	}
}

func TestEnvelopeParsesBrokerPayload(t *testing.T) {
	// Exactly the shape the broker writes (src/compiler/envelope.ts).
	payload := []byte(`{
		"sources": {
			"dsireusa": {
				"states": ["MI","OH"],
				"utilities": [],
				"service_areas": [],
				"zip_codes": ["48201"],
				"unbounded": false,
				"subscribers": 3
			}
		},
		"unsubscribedSources": ["srp","pnm"]
	}`)

	var e DemandEnvelope
	if err := json.Unmarshal(payload, &e); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	d := e.Sources["dsireusa"]
	if !reflect.DeepEqual(d.States, []string{"MI", "OH"}) {
		t.Errorf("states = %v", d.States)
	}
	if !reflect.DeepEqual(d.ZipCodes, []string{"48201"}) {
		t.Errorf("zips = %v", d.ZipCodes)
	}
	if d.Unbounded {
		t.Error("unbounded should be false")
	}
	if d.Subscribers != 3 {
		t.Errorf("subscribers = %d, want 3", d.Subscribers)
	}
	if !reflect.DeepEqual(e.UnsubscribedSources, []string{"srp", "pnm"}) {
		t.Errorf("unsubscribed = %v", e.UnsubscribedSources)
	}
}

func TestEnvelopeCarriesNoTenantIdentities(t *testing.T) {
	// Structural guard: the type has no field that could hold a tenant id.
	// If someone adds one, this fails and they have to justify it.
	demand := reflect.TypeOf(SourceDemand{})
	for i := 0; i < demand.NumField(); i++ {
		name := demand.Field(i).Name
		switch name {
		case "States", "Utilities", "ServiceAreas", "ZipCodes", "Unbounded", "Subscribers":
			// Territory, a flag, and a count. No identities.
		default:
			t.Errorf("SourceDemand gained field %q — the envelope must stay tenant-blind; "+
				"a collector must not be able to tell whether one tenant or ten asked", name)
		}
	}
}
