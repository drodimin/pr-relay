package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDefaults(t *testing.T) {
	d := Defaults()
	if d.Poll.BudgetPct != 0.10 || d.Poll.LowBudgetThreshold != 0.20 {
		t.Fatalf("budget defaults wrong: %+v", d.Poll)
	}
	if d.Poll.MinInterval.Duration != time.Minute || d.Poll.MaxInterval.Duration != 16*time.Minute ||
		d.Poll.MaxStretchedInterval.Duration != 60*time.Minute {
		t.Fatalf("interval defaults wrong: %+v", d.Poll)
	}
	if d.Poll.BatchSize != 20 || d.Subscriptions.PendingCap != 200 {
		t.Fatalf("size defaults wrong: %+v", d)
	}
	if d.Delivery.QuietPeriod.Duration != 10*time.Second {
		t.Fatalf("quiet_period default = %v, want 10s", d.Delivery.QuietPeriod)
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
}

func TestLoadMissing(t *testing.T) {
	cfg, warns, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(warns) != 0 {
		t.Fatalf("warnings = %v", warns)
	}
	if !reflect.DeepEqual(cfg, Defaults()) {
		t.Fatalf("cfg = %+v, want defaults", cfg)
	}
}

func TestLoadDurations(t *testing.T) {
	t.Setenv(EnvFastPoll, "1")
	p := writeConfig(t, "[poll]\nmin_interval = \"100ms\"\nmax_interval = \"200ms\"\n")
	cfg, warns, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(warns) != 0 {
		t.Fatalf("warnings = %v", warns)
	}
	if cfg.Poll.MinInterval.Duration != 100*time.Millisecond || cfg.Poll.MaxInterval.Duration != 200*time.Millisecond {
		t.Fatalf("intervals = %v / %v", cfg.Poll.MinInterval, cfg.Poll.MaxInterval)
	}
	// Unset fields keep their defaults.
	if cfg.Poll.BatchSize != DefaultBatchSize || cfg.Subscriptions.PendingCap != DefaultPendingCap {
		t.Fatalf("defaults lost: %+v", cfg)
	}
}

func TestLoadAllFields(t *testing.T) {
	p := writeConfig(t, `
[poll]
min_interval = "30s"
max_interval = "5m"
max_stretched_interval = "10m"
budget_pct = 0.5
low_budget_threshold = 0.1
batch_size = 5

[subscriptions]
pending_cap = 7

[delivery]
quiet_period = "30s"
`)
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Config{
		Poll: PollConfig{
			MinInterval:          Duration{30 * time.Second},
			MaxInterval:          Duration{5 * time.Minute},
			MaxStretchedInterval: Duration{10 * time.Minute},
			BudgetPct:            0.5,
			LowBudgetThreshold:   0.1,
			BatchSize:            5,
		},
		Subscriptions: SubsConfig{PendingCap: 7},
		Delivery:      DeliveryConfig{QuietPeriod: Duration{30 * time.Second}},
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("cfg = %+v, want %+v", cfg, want)
	}
}

func TestLoadUnknownKey(t *testing.T) {
	p := writeConfig(t, "[poll]\nbatch_size = 3\nbogus = 1\n")
	cfg, warns, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Poll.BatchSize != 3 {
		t.Fatalf("batch_size = %d", cfg.Poll.BatchSize)
	}
	if len(warns) != 1 || warns[0] != `unknown config key "poll.bogus"` {
		t.Fatalf("warnings = %v, want one naming poll.bogus", warns)
	}
}

func TestLoadInvalid(t *testing.T) {
	tests := []struct {
		name, body, field string
	}{
		{"zero min", "[poll]\nmin_interval = \"0s\"\n", "min_interval"},
		{"negative min", "[poll]\nmin_interval = \"-1s\"\n", "min_interval"},
		{"max below min", "[poll]\nmin_interval = \"2m\"\nmax_interval = \"1m\"\n", "max_interval"},
		{"stretched below max", "[poll]\nmax_stretched_interval = \"1m\"\n", "max_stretched_interval"},
		{"budget zero", "[poll]\nbudget_pct = 0.0\n", "budget_pct"},
		{"budget over one", "[poll]\nbudget_pct = 1.5\n", "budget_pct"},
		{"low negative", "[poll]\nlow_budget_threshold = -0.1\n", "low_budget_threshold"},
		{"low one", "[poll]\nlow_budget_threshold = 1.0\n", "low_budget_threshold"},
		{"batch zero", "[poll]\nbatch_size = 0\n", "batch_size"},
		{"batch too big", "[poll]\nbatch_size = 21\n", "batch_size"},
		{"pending zero", "[subscriptions]\npending_cap = 0\n", "pending_cap"},
		{"bad duration", "[poll]\nmin_interval = \"soon\"\n", "min_interval"},
		{"quiet zero", "[delivery]\nquiet_period = \"0s\"\n", "quiet_period"},
		{"quiet negative", "[delivery]\nquiet_period = \"-1s\"\n", "quiet_period"},
		{"quiet too short", "[delivery]\nquiet_period = \"500ms\"\n", "quiet_period"},
		{"quiet too long", "[delivery]\nquiet_period = \"6m\"\n", "quiet_period"},
		{"quiet bad duration", "[delivery]\nquiet_period = \"soon\"\n", "quiet_period"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Load(writeConfig(t, tc.body))
			if err == nil {
				t.Fatal("Load accepted invalid config")
			}
			if !strings.HasPrefix(err.Error(), "config: ") || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("err = %q, want config: prefix naming %s", err, tc.field)
			}
		})
	}
}

func TestLoadBoundsInclusive(t *testing.T) {
	body := "[poll]\nbudget_pct = 1.0\nlow_budget_threshold = 0.0\nbatch_size = 1\n[subscriptions]\npending_cap = 1\n"
	if _, _, err := Load(writeConfig(t, body)); err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Setenv(EnvFastPoll, "")
	for _, v := range []string{"1s", "5m"} {
		if _, _, err := Load(writeConfig(t, "[delivery]\nquiet_period = \""+v+"\"\n")); err != nil {
			t.Fatalf("quiet_period %s: %v", v, err)
		}
	}
}

func TestDurationRoundTrip(t *testing.T) {
	d := Duration{90 * time.Second}
	b, err := d.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	var got Duration
	if err := got.UnmarshalText(b); err != nil {
		t.Fatal(err)
	}
	if got != d {
		t.Fatalf("round trip = %v, want %v", got, d)
	}
}

// TestMinIntervalFloor covers review WR-07: config.toml alone cannot set
// min_interval below MinMinInterval; only the test-only EnvFastPoll can.
func TestMinIntervalFloor(t *testing.T) {
	t.Setenv(EnvFastPoll, "")
	for _, v := range []string{"1ms", "100ms", "9s"} {
		p := writeConfig(t, "[poll]\nmin_interval = \""+v+"\"\n")
		if _, _, err := Load(p); err == nil || !strings.Contains(err.Error(), "min_interval") {
			t.Errorf("min_interval %s: err = %v, want a min_interval error", v, err)
		}
	}
	p := writeConfig(t, "[poll]\nmin_interval = \"10s\"\n")
	if _, _, err := Load(p); err != nil {
		t.Errorf("min_interval 10s: %v", err)
	}
	t.Setenv(EnvFastPoll, "1")
	p = writeConfig(t, "[poll]\nmin_interval = \"100ms\"\nmax_interval = \"200ms\"\n")
	if _, _, err := Load(p); err != nil {
		t.Errorf("min_interval 100ms with %s=1: %v", EnvFastPoll, err)
	}
}

// TestQuietPeriod covers D-09: the delivery quiet period defaults to 10s, is
// bounded to [1s, 5m], and only EnvFastPoll lowers the floor.
func TestQuietPeriod(t *testing.T) {
	t.Setenv(EnvFastPoll, "")
	cfg, _, err := Load(writeConfig(t, "[poll]\nbatch_size = 3\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Delivery.QuietPeriod.Duration != DefaultQuietPeriod {
		t.Fatalf("quiet_period without [delivery] = %v, want %v", cfg.Delivery.QuietPeriod, DefaultQuietPeriod)
	}

	cfg, warns, err := Load(writeConfig(t, "[delivery]\nquiet_period = \"30s\"\n"))
	if err != nil || len(warns) != 0 {
		t.Fatalf("Load 30s: %v, warnings %v", err, warns)
	}
	if cfg.Delivery.QuietPeriod.Duration != 30*time.Second {
		t.Fatalf("quiet_period = %v, want 30s", cfg.Delivery.QuietPeriod)
	}

	_, _, err = Load(writeConfig(t, "[delivery]\nquiet_period = \"500ms\"\n"))
	if err == nil || !strings.Contains(err.Error(), "quiet_period") || !strings.Contains(err.Error(), "at least 1s") {
		t.Fatalf("500ms: err = %v, want quiet_period at least 1s", err)
	}
	_, _, err = Load(writeConfig(t, "[delivery]\nquiet_period = \"6m\"\n"))
	if err == nil || !strings.Contains(err.Error(), "quiet_period") || !strings.Contains(err.Error(), "at most 5m") {
		t.Fatalf("6m: err = %v, want quiet_period at most 5m", err)
	}

	t.Setenv(EnvFastPoll, "1")
	cfg, _, err = Load(writeConfig(t, "[delivery]\nquiet_period = \"5ms\"\n"))
	if err != nil {
		t.Fatalf("5ms with %s=1: %v", EnvFastPoll, err)
	}
	if cfg.Delivery.QuietPeriod.Duration != 5*time.Millisecond {
		t.Fatalf("quiet_period = %v, want 5ms", cfg.Delivery.QuietPeriod)
	}
	_, _, err = Load(writeConfig(t, "[delivery]\nquiet_period = \"6m\"\n"))
	if err == nil || !strings.Contains(err.Error(), "at most 5m") {
		t.Fatalf("6m with %s=1: err = %v, want the 5m maximum to still apply", EnvFastPoll, err)
	}
}
