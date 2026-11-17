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
//   - D-09 delivery quiet period: [delivery] quiet_period (default 10s) is how
//     long a pane must be quiet before an update is typed into it, so a push
//     never collides with someone typing. It must be in [1s, 5m]; only the
//     test-only EnvFastPoll lowers the 1s floor.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"strconv"
	"strings"
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
	DefaultQuietPeriod          = 10 * time.Second // D-09 typing-collision quiet period
)

// Bounds for quiet_period (D-09). A zero value would turn the delivery gate
// into a hot loop; a huge one would stall delivery indefinitely.
const (
	MinQuietPeriod = time.Second
	MaxQuietPeriod = 5 * time.Minute
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

// quietPeriodFloor returns the smallest quiet_period Validate accepts.
func quietPeriodFloor() time.Duration {
	if os.Getenv(EnvFastPoll) == "1" {
		return fastPollFloor
	}
	return MinQuietPeriod
}

// Lifecycle defaults and bounds (D-15, LIFE-06, LIFE-07). The floor stops a
// typo such as "1m" from flooding an agent with check-ins; the ceilings stop
// a subscription from outliving any plausible need for it.
const (
	DefaultCheckinAfter = 24 * time.Hour
	DefaultExpireAfter  = 7 * 24 * time.Hour
	MinCheckinAfter     = time.Hour
	MaxCheckinAfter     = 30 * 24 * time.Hour
	MaxExpireAfter      = 90 * 24 * time.Hour
)

// lifecycleFloor returns the smallest checkin_after Validate accepts.
func lifecycleFloor() time.Duration {
	if os.Getenv(EnvFastPoll) == "1" {
		return fastPollFloor
	}
	return MinCheckinAfter
}

// maxDays keeps n*24h inside time.Duration's range.
const maxDays = int64(math.MaxInt64 / int64(24*time.Hour))

// Duration is a time.Duration that decodes from a Go duration string or a
// whole number of days ("7d").
type Duration struct{ time.Duration }

// UnmarshalText parses a duration string such as "100ms", "16m" or "7d". The
// day form is a positive whole number followed by a single "d".
func (d *Duration) UnmarshalText(b []byte) error {
	s := string(b)
	if n, ok := strings.CutSuffix(s, "d"); ok {
		if n == "" || strings.Trim(n, "0123456789") != "" {
			return fmt.Errorf("invalid duration %q: day form must be a whole number of days, such as \"7d\"", s)
		}
		days, err := strconv.ParseInt(n, 10, 64)
		if err != nil || days < 1 || days > maxDays {
			return fmt.Errorf("invalid duration %q: day count out of range", s)
		}
		d.Duration = time.Duration(days) * 24 * time.Hour
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

// MarshalText renders the duration in Go duration syntax.
func (d Duration) MarshalText() ([]byte, error) {
	return []byte(d.String()), nil
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

// DeliveryConfig is the [delivery] table.
type DeliveryConfig struct {
	QuietPeriod Duration `toml:"quiet_period"`
}

// LifecycleConfig is the [lifecycle] table (D-15, LIFE-07).
type LifecycleConfig struct {
	// CheckinAfter is how long a subscription may go without a delivered
	// event before the agent is asked whether it still needs it (LIFE-06).
	CheckinAfter Duration `toml:"checkin_after"`
	// ExpireAfter is how long a subscription may go without activity before
	// it is removed with a final message (LIFE-07). It must exceed
	// CheckinAfter.
	ExpireAfter Duration `toml:"expire_after"`
}

// Config is the whole config.toml.
type Config struct {
	Poll          PollConfig      `toml:"poll"`
	Subscriptions SubsConfig      `toml:"subscriptions"`
	Delivery      DeliveryConfig  `toml:"delivery"`
	Lifecycle     LifecycleConfig `toml:"lifecycle"`
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
		Delivery:      DeliveryConfig{QuietPeriod: Duration{DefaultQuietPeriod}},
		Lifecycle: LifecycleConfig{
			CheckinAfter: Duration{DefaultCheckinAfter},
			ExpireAfter:  Duration{DefaultExpireAfter},
		},
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

// Validate rejects values that would make the poller or the delivery gate
// spin, stall or overspend.
func (c Config) Validate() error {
	p := c.Poll
	floor := minIntervalFloor()
	qp := c.Delivery.QuietPeriod.Duration
	qpFloor := quietPeriodFloor()
	ci, ex := c.Lifecycle.CheckinAfter.Duration, c.Lifecycle.ExpireAfter.Duration
	ciFloor := lifecycleFloor()
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
	case qp < qpFloor:
		return fmt.Errorf("config: quiet_period: must be at least %s, got %s", qpFloor, qp)
	case qp > MaxQuietPeriod:
		return fmt.Errorf("config: quiet_period: must be at most %s, got %s", MaxQuietPeriod, qp)
	case ci < ciFloor:
		return fmt.Errorf("config: lifecycle.checkin_after: must be at least %s, got %s", ciFloor, ci)
	case ci > MaxCheckinAfter:
		return fmt.Errorf("config: lifecycle.checkin_after: must be at most %s, got %s", MaxCheckinAfter, ci)
	case ex <= ci:
		return fmt.Errorf("config: lifecycle.expire_after: must be greater than checkin_after %s, got %s", ci, ex)
	case ex > MaxExpireAfter:
		return fmt.Errorf("config: lifecycle.expire_after: must be at most %s, got %s", MaxExpireAfter, ex)
	}
	return nil
}
