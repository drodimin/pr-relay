package deliver

import (
	"testing"

	"github.com/drodimin/pr-relay/internal/herdr"
	"github.com/drodimin/pr-relay/internal/store"
)

func TestIdentity(t *testing.T) {
	sess := func(v string) *herdr.AgentSession {
		return &herdr.AgentSession{Source: "x", Kind: "session_id", Value: v}
	}
	cases := []struct {
		name        string
		storedSess  string
		storedKind  string
		curAgent    string
		curSess     *herdr.AgentSession
		curTerminal string
		want        identity
	}{
		{"same session", "s1", "claude", "claude", sess("s1"), "", identMatch},
		{"different session", "s1", "claude", "claude", sess("s2"), "", identMismatch},
		{"session not identified, same kind", "s1", "claude", "claude", nil, "", identUnidentified},
		{"session not identified, other kind", "s1", "claude", "codex", nil, "", identMismatch},
		{"session not identified, no stored kind", "s1", "", "codex", nil, "", identUnidentified},
		{"empty session value counts as unidentified", "s1", "claude", "claude", sess(""), "", identUnidentified},
		{"no stored session, same kind", "", "codex", "codex", sess("zz"), "", identMatch},
		{"no stored session, same kind, nil session", "", "codex", "codex", nil, "", identMatch},
		{"no stored session, other kind", "", "codex", "claude", sess("zz"), "", identMismatch},
		{"legacy sub matches anything", "", "", "claude", sess("zz"), "", identMatch},
		{"legacy sub matches nil session", "", "", "codex", nil, "", identMatch},
		{"terminal id ignored on match", "s1", "claude", "claude", sess("s1"), "term-other", identMatch},
		{"terminal id ignored on mismatch", "s1", "claude", "claude", sess("s2"), "term-same", identMismatch},
		{"terminal id ignored on kind match", "", "codex", "codex", nil, "term-other", identMatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sub := store.Subscription{ID: "sub", AgentSession: tc.storedSess, AgentKind: tc.storedKind, TerminalID: "term-same"}
			info := herdr.AgentInfo{Agent: tc.curAgent, AgentSession: tc.curSess, TerminalID: tc.curTerminal, Status: herdr.StatusIdle}
			if got := checkIdentity(sub, info); got != tc.want {
				t.Fatalf("checkIdentity = %v, want %v", got, tc.want)
			}
		})
	}
}
