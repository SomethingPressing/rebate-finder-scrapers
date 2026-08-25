package db

import "testing"

// The lock id is a constant that two processes must agree on. If it ever
// changes, every collector already running keeps the old one and the two
// schedules stop excluding each other — silently, because both passes still
// work and the only symptom is double the upstream requests.
func TestCollectionLockIDIsStable(t *testing.T) {
	if collectionLockID != 8_147_320_461 {
		t.Fatalf("the collection lock id changed to %d — every collector still running holds the old one, "+
			"so two schedules would stop excluding each other. Change it only with a full fleet restart.",
			collectionLockID)
	}
}

// Releasing when nothing is held has to be free. UnlockCollection is called
// from a deferred function that runs whether or not the lock was taken — a
// failed lock attempt still ends the pass — so this is the common path, not
// an edge case.
func TestUnlockWithoutLockIsANoOp(t *testing.T) {
	d := &DB{}
	if err := d.UnlockCollection(); err != nil {
		t.Fatalf("unlocking with nothing held returned %v, want nil", err)
	}
	if d.lockConn != nil {
		t.Fatal("unlocking with nothing held left a connection behind")
	}
}
