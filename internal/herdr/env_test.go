package herdr_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/drodimin/pr-relay/internal/herdr"
)

// env returns a map-backed getenv; tests never read the real environment,
// so HERDR_* set by the pane running the tests cannot leak in.
func env(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func TestResolveSocket(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"socket path cleaned", map[string]string{"HERDR_SOCKET_PATH": "/a/b/../h.sock", "HERDR_SESSION": "x", "HOME": "/home/u"}, "/a/h.sock"},
		{"relative socket path", map[string]string{"HERDR_SOCKET_PATH": "rel/h.sock"}, filepath.Join(wd, "rel/h.sock")},
		{"session", map[string]string{"HERDR_SESSION": "work", "HOME": "/home/u"}, "/home/u/.config/herdr/sessions/work/herdr.sock"},
		{"default", map[string]string{"HOME": "/home/u/"}, "/home/u/.config/herdr/herdr.sock"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := herdr.ResolveSocket(env(c.env))
			if err != nil {
				t.Fatalf("ResolveSocket: %v", err)
			}
			if got != c.want {
				t.Fatalf("ResolveSocket = %q, want %q", got, c.want)
			}
		})
	}
	t.Run("no home", func(t *testing.T) {
		if got, err := herdr.ResolveSocket(env(nil)); err == nil {
			t.Fatalf("ResolveSocket = %q, want an error with HOME unset", got)
		}
		if got, err := herdr.ResolveSocket(env(map[string]string{"HERDR_SESSION": "w"})); err == nil {
			t.Fatalf("ResolveSocket = %q, want an error with HOME unset", got)
		}
	})
}

func TestPaneFromEnv(t *testing.T) {
	if got := herdr.PaneFromEnv(env(map[string]string{"HERDR_PANE_ID": "w1:p3"})); got != "w1:p3" {
		t.Fatalf("PaneFromEnv = %q, want w1:p3", got)
	}
	if got := herdr.PaneFromEnv(env(nil)); got != "" {
		t.Fatalf("PaneFromEnv = %q, want empty", got)
	}
	if !herdr.InHerdr(env(map[string]string{"HERDR_PANE_ID": "w1:p3"})) {
		t.Fatal("InHerdr = false with HERDR_PANE_ID set")
	}
	if herdr.InHerdr(env(map[string]string{"HERDR_SOCKET_PATH": "/s"})) {
		t.Fatal("InHerdr = true without HERDR_PANE_ID")
	}
}
