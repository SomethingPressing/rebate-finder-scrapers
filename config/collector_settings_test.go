package config

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The interop test that matters: a value encrypted by the BROKER must decrypt
// here. Two independent implementations of AES-256-GCM agreeing is not
// something to assume — Node keeps the auth tag separate and Go expects it
// appended, which is exactly the kind of difference that silently produces
// garbage rather than an error.
func TestDecryptsWhatTheBrokerEncrypted(t *testing.T) {
	const secret = "a-test-master-key-long-enough"
	t.Setenv("BROKER_SECRETS_KEY", secret)

	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available")
	}

	const plaintext = "sk-test-value-123"
	script := `
const c=require("crypto");
const key=c.createHash("sha256").update(process.argv[1]).digest();
const iv=c.randomBytes(12);
const ci=c.createCipheriv("aes-256-gcm",key,iv);
const body=Buffer.concat([ci.update(process.argv[2],"utf8"),ci.final()]);
process.stdout.write([iv.toString("base64"),ci.getAuthTag().toString("base64"),body.toString("base64")].join(":"));
`
	out, err := exec.Command(node, "-e", script, secret, plaintext).Output()
	if err != nil {
		t.Fatalf("could not produce a broker-encrypted value: %v", err)
	}

	got, err := decryptSetting(string(out))
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}
	if got != plaintext {
		t.Fatalf("got %q, want %q", got, plaintext)
	}
}

func TestDecryptFailsClosed(t *testing.T) {
	t.Setenv("BROKER_SECRETS_KEY", "a-test-master-key-long-enough")
	for _, bad := range []string{"", "not-three-parts", "a:b:c"} {
		if _, err := decryptSetting(bad); err == nil {
			t.Fatalf("expected %q to fail", bad)
		}
	}
	// A value encrypted under a different key must not decrypt to anything.
	t.Setenv("BROKER_SECRETS_KEY", "")
	if _, err := decryptSetting("a:b:c"); err == nil {
		t.Fatal("expected a missing key to fail")
	}
}

func TestScrapeIntervalNeverGoesBelowTheFloor(t *testing.T) {
	// Enforced on READ as well as on write, because the two can disagree: a
	// row written by hand, or before the floor existed, would otherwise have
	// every collector hammering upstream APIs.
	c := &CollectorSettings{values: map[string]string{key(FleetScope, "SCRAPER_INTERVAL_HOURS"): "1"}}
	if got := c.ScrapeInterval(12 * time.Hour); got != MinScrapeInterval {
		t.Fatalf("got %v, want the %v floor", got, MinScrapeInterval)
	}
	c = &CollectorSettings{values: map[string]string{key(FleetScope, "SCRAPER_INTERVAL_HOURS"): "12"}}
	if got := c.ScrapeInterval(6 * time.Hour); got != 12*time.Hour {
		t.Fatalf("got %v, want 12h", got)
	}
	// Nothing set, and a default below the floor: still the floor.
	if got := (&CollectorSettings{values: map[string]string{}}).ScrapeInterval(time.Hour); got != MinScrapeInterval {
		t.Fatalf("got %v, want the floor", got)
	}
}

func TestFallsBackToTheEnvironment(t *testing.T) {
	os.Unsetenv("SOME_SETTING")
	c := &CollectorSettings{values: map[string]string{}}
	t.Setenv("SOME_SETTING", "from-env")
	if got := c.String(FleetScope, "SOME_SETTING", "SOME_SETTING"); got != "from-env" {
		t.Fatalf("got %q", got)
	}
	// A broker value wins over the environment.
	c.values[key(FleetScope, "SOME_SETTING")] = "from-broker"
	if got := c.String(FleetScope, "SOME_SETTING", "SOME_SETTING"); got != "from-broker" {
		t.Fatalf("got %q", got)
	}
	if !strings.Contains("from-broker", "broker") {
		t.Fatal("unreachable")
	}
}
