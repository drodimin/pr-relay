// Package config loads the optional config.toml that tunes the poller
// (D-08). Every knob has a default, so a missing file is not an error.
//
// Resolved choices (phase 02 CONTEXT):
//   - D-05 budget: the poller spends at most budget_pct (default 10%) of the
//     hourly GraphQL budget.
//   - D-06 low budget: below low_budget_threshold (default 20%) remaining,
//     intervals stretch up to max_stretched_interval (default 60m).
//   - D-07 cadence: per-PR intervals back off from min_interval (default 1m)
//     to max_interval (default 16m); batches hold up to batch_size PRs
//     (default 20).
//   - D-08 config: durations are Go duration strings ("30s", "16m");
//     unknown keys only warn; out-of-range values are rejected so a bad file
//     cannot make the poller spin or stall. min_interval must be at least
//     MinMinInterval (10s); only the test-only EnvFastPoll lowers it. pending_cap (default 200) bounds
//     each subscription's undelivered queue.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/BurntSushi/toml"
)

// Defaults for every knob (D-05..D-08).
const (
	DefaultBudgetPct            = 0.10             // share of the hourly API budget the poller may spend
	DefaultLowBudgetThreshold   = 0.20             // remaining share below which intervals stretch
	DefaultMinInterval          = time.Minute      // fastest per-PR poll interval
	DefaultMaxInterval          = 16 * time.Minute // slowest per-PR interval under a normal budget
	DefaultMaxStretchedInterval = 60 * time.Minute // slowest per-PR interval under a low budget
	DefaultBatchSize            = 20               // PRs per batched GraphQL query
	DefaultPendingCap           = 200              // undelivered events kept per subscription
)

// MaxBatchSize is the largest accepted batch_size.
const MaxBatchSize = 20

// MinMinInterval is the smallest accepted min_interval (review WR-07). A
// typo such as "1ms" would otherwise make every PR due again at once and send
// batches back-to-back until the spend cap stops them.
const MinMinInterval = 10 * time.Second

// EnvFastPoll, set to "1", lowers the min_interval floor to 1ms. It is a
// test-only override for the crash and dev-harness tests, which drive the
// poller with 100ms cadences; config.toml alone can never go below
// MinMinInterval.
const EnvFastPoll = "PR_RELAY_TEST_FAST_POLL"

const fastPollFloor = time.Millisecond

// minIntervalFloor returns the smallest min_interval Validate accepts.
func minIntervalFloor() time.Duration {
	if os.Getenv(EnvFastPoll) == "1" {
		return fastPollFloor
	}
	return MinMinInterval
}

// Duration is a time.Duration that decodes from a Go duration string.
type Duration struct{ time.Duration }

// UnmarshalText parses a duration string such as "100ms" or "16m".
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

// MarshalText renders the duration in Go duration syntax.
func (d Duration) MarshalText() ([]byte, error) {
	return []byte(d.Duration.String()), nil
}

// PollConfig is the [poll] table.
type PollConfig struct {
	MinInterval          Duration `toml:"min_interval"`
	MaxInterval          Duration `toml:"max_interval"`
	MaxStretchedInterval Duration `toml:"max_stretched_interval"`
	BudgetPct            float64  `toml:"budget_pct"`
	LowBudgetThreshold   float64  `toml:"low_budget_threshold"`
	BatchSize            int      `toml:"batch_size"`
}

// SubsConfig is the [subscriptions] table.
type SubsConfig struct {
	PendingCap int `toml:"pending_cap"`
}

// Config is the whole config.toml.
type Config struct {
	Poll          PollConfig `toml:"poll"`
	Subscriptions SubsConfig `toml:"subscriptions"`
}

// Defaults returns a Config with every knob at its default.
func Defaults() Config {
	return Config{
		Poll: PollConfig{
			MinInterval:          Duration{DefaultMinInterval},
			MaxInterval:          Duration{DefaultMaxInterval},
			MaxStretchedInterval: Duration{DefaultMaxStretchedInterval},
			BudgetPct:            DefaultBudgetPct,
			LowBudgetThreshold:   DefaultLowBudgetThreshold,
			BatchSize:            DefaultBatchSize,
		},
		Subscriptions: SubsConfig{PendingCap: DefaultPendingCap},
	}
}

// Load reads path over the defaults. A missing file yields Defaults(). The
// returned warnings name unknown keys; an invalid file or value is an error.
func Load(path string) (Config, []string, error) {
	cfg := Defaults()
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cfg, nil, nil
		}
		return Config{}, nil, fmt.Errorf("config: stat %s: %w", path, err)
	}
	md, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return Config{}, nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	var warns []string
	for _, k := range md.Undecoded() {
		warns = append(warns, fmt.Sprintf("unknown config key %q", k.String()))
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, warns, err
	}
	return cfg, warns, nil
}

// Validate rejects values that would make the poller spin, stall or
// overspend.
func (c Config) Validate() error {
	p := c.Poll
	floor := minIntervalFloor()
	switch {
	case p.MinInterval.Duration < floor:
		return fmt.Errorf("config: min_interval: must be at least %s, got %s", floor, p.MinInterval.Duration)
	case p.MaxInterval.Duration < p.MinInterval.Duration:
		return fmt.Errorf("config: max_interval: %s is below min_interval %s", p.MaxInterval.Duration, p.MinInterval.Duration)
	case p.MaxStretchedInterval.Duration < p.MaxInterval.Duration:
		return fmt.Errorf("config: max_stretched_interval: %s is below max_interval %s", p.MaxStretchedInterval.Duration, p.MaxInterval.Duration)
	case !(p.BudgetPct > 0 && p.BudgetPct <= 1):
		return fmt.Errorf("config: budget_pct: must be in (0,1], got %v", p.BudgetPct)
	case !(p.LowBudgetThreshold >= 0 && p.LowBudgetThreshold < 1):
		return fmt.Errorf("config: low_budget_threshold: must be in [0,1), got %v", p.LowBudgetThreshold)
	case p.BatchSize < 1 || p.BatchSize > MaxBatchSize:
		return fmt.Errorf("config: batch_size: must be 1..%d, got %d", MaxBatchSize, p.BatchSize)
	case c.Subscriptions.PendingCap < 1:
		return fmt.Errorf("config: pending_cap: must be at least 1, got %d", c.Subscriptions.PendingCap)
	}
	return nil
}
