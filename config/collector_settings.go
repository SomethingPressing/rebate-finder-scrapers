package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Operational settings handed down by the broker.
//
// The demand envelope says WHAT to collect. This says HOW: schedule,
// concurrency, a source's own API key. Both are read straight from the staging
// database this collector is already connected to — there is no API call and
// no new credential.
//
// Precedence is broker value, then this process's own environment. The
// fallback is the point, not a nicety: a broker that is mid-migration, or a
// setting nobody has filled in, must leave the collector running on whatever
// it already had rather than starting with nothing.
//
// Still tenant-blind. Scope is "*" or a source name, never a tenant, so
// reading settings tells a collector nothing about who its work serves.

const collectorSettingsTable = "broker.collector_settings"

// FleetScope covers every collector.
const FleetScope = "*"

// MinScrapeInterval is a floor, enforced on read as well as on write.
//
// On read too, because the two sides can disagree: a value written before the
// floor existed, or straight into the table by hand, would otherwise have every
// collector hammering upstream APIs. Sources rate-limit, and collecting more
// often does not produce fresher data — the listings do not change that fast.
const MinScrapeInterval = 6 * time.Hour

// CollectorSettings is what the broker had to say, already decrypted.
type CollectorSettings struct {
	values map[string]string // "scope\x00name" -> value
}

func key(scope, name string) string { return scope + "\x00" + name }

// LoadCollectorSettings reads what applies to this collector.
//
// Returns an empty set rather than an error when the table is missing or
// unreadable: a collector deployed against a database whose broker migrations
// have not run must keep working from its own environment.
func LoadCollectorSettings(db *gorm.DB, sources []string) *CollectorSettings {
	out := &CollectorSettings{values: map[string]string{}}
	if db == nil {
		return out
	}

	scopes := append([]string{FleetScope}, sources...)
	var rows []struct {
		Scope      string `gorm:"column:scope"`
		Name       string `gorm:"column:name"`
		Ciphertext string `gorm:"column:ciphertext"`
	}
	q := fmt.Sprintf("SELECT scope, name, ciphertext FROM %s WHERE scope IN ?", collectorSettingsTable)
	if err := db.Raw(q, scopes).Scan(&rows).Error; err != nil {
		return out
	}

	for _, r := range rows {
		v, err := decryptSetting(r.Ciphertext)
		if err != nil {
			// A value we cannot read is omitted, so the environment fallback
			// applies. Returning "" would look like a deliberate blank.
			continue
		}
		out.values[key(r.Scope, r.Name)] = v
	}
	return out
}

// String returns the broker's value, falling back to the environment.
func (c *CollectorSettings) String(scope, name, envName string) string {
	if c != nil {
		if v, ok := c.values[key(scope, name)]; ok && v != "" {
			return v
		}
	}
	return os.Getenv(envName)
}

// Int returns the broker's value as an int, falling back to the environment
// and then to def.
func (c *CollectorSettings) Int(scope, name, envName string, def int) int {
	raw := c.String(scope, name, envName)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 1 {
		return def
	}
	return n
}

// ScrapeInterval is the fleet schedule, never shorter than MinScrapeInterval.
func (c *CollectorSettings) ScrapeInterval(def time.Duration) time.Duration {
	raw := c.String(FleetScope, "SCRAPER_INTERVAL_HOURS", "SCRAPER_INTERVAL_HOURS")
	if raw == "" {
		if def < MinScrapeInterval {
			return MinScrapeInterval
		}
		return def
	}
	h, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || h <= 0 {
		return def
	}
	d := time.Duration(h * float64(time.Hour))
	if d < MinScrapeInterval {
		return MinScrapeInterval
	}
	return d
}

// decryptSetting mirrors the broker's AES-256-GCM format exactly:
// base64(iv) : base64(tag) : base64(ciphertext), with the key being the
// SHA-256 of BROKER_SECRETS_KEY. Any mismatch fails closed.
func decryptSetting(stored string) (string, error) {
	raw := strings.TrimSpace(os.Getenv("BROKER_SECRETS_KEY"))
	if raw == "" {
		return "", fmt.Errorf("BROKER_SECRETS_KEY is not set")
	}
	sum := sha256.Sum256([]byte(raw))

	parts := strings.Split(stored, ":")
	if len(parts) != 3 {
		return "", fmt.Errorf("malformed value")
	}
	iv, err := base64.StdEncoding.DecodeString(parts[0])
	if err != nil {
		return "", err
	}
	tag, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return "", err
	}
	body, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCMWithNonceSize(block, len(iv))
	if err != nil {
		return "", err
	}
	// Go's GCM expects the tag appended to the ciphertext; the Node API keeps
	// them apart, which is why they are stored separately and joined here.
	plain, err := gcm.Open(nil, iv, append(body, tag...), nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
