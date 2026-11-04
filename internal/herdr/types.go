package herdr

import "encoding/json"

// Protocol is the herdr socket protocol version this client was verified
// against (herdr 0.9.5).
const Protocol = 22

// Agent status values reported in agent_status.
const (
	StatusIdle    = "idle"
	StatusWorking = "working"
	StatusBlocked = "blocked"
	// StatusDone means idle and not yet seen by the user (reported on
	// unfocused panes).
	StatusDone    = "done"
	StatusUnknown = "unknown"
)

// AgentSession identifies the agent's session, when herdr knows it.
type AgentSession struct {
	Source string `json:"source"`
	Agent  string `json:"agent"`
	Kind   string `json:"kind"`
	Value  string `json:"value"`
}

// AgentInfo is the "agent" payload of agent.get, agent.wait and
// agent.prompt results. Unknown fields are ignored.
type AgentInfo struct {
	PaneID      string `json:"pane_id"`
	TabID       string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	// Agent is the agent kind, e.g. "claude" or "codex".
	Agent string `json:"agent"`
	// AgentSession is nil when herdr has not identified the session.
	AgentSession *AgentSession `json:"agent_session"`
	Status       string        `json:"agent_status"`
	// StateChangeSeq increases on every status change (0 when absent).
	StateChangeSeq uint64 `json:"state_change_seq"`
	// CompletionSeq is nil until the agent has completed a turn.
	CompletionSeq *uint64 `json:"completion_seq"`
	TerminalID    string  `json:"terminal_id"`
	Focused       bool    `json:"focused"`
	LaunchPending bool    `json:"launch_pending"`
	// InteractiveReady is tri-state. herdr omits the key for long-running
	// agents (observed on 0.9.5), so nil means "not reported" and only an
	// explicit false means the agent is not ready for input.
	InteractiveReady *bool `json:"interactive_ready"`
}

// PaneInfo is one pane from pane.get or pane.list. On a bare shell Agent is
// empty, AgentSession is nil and Status is "unknown".
type PaneInfo struct {
	PaneID       string        `json:"pane_id"`
	TabID        string        `json:"tab_id"`
	WorkspaceID  string        `json:"workspace_id"`
	Agent        string        `json:"agent"`
	AgentSession *AgentSession `json:"agent_session"`
	Status       string        `json:"agent_status"`
	TerminalID   string        `json:"terminal_id"`
}

// WaitOpts is the wait clause of agent.prompt (and the until/timeout part of
// agent.wait).
type WaitOpts struct {
	Until     []string `json:"until"`
	TimeoutMs int64    `json:"timeout_ms"`
}

// PromptParams are the agent.prompt params. A nil Wait omits the clause.
type PromptParams struct {
	Target string    `json:"target"`
	Text   string    `json:"text"`
	Wait   *WaitOpts `json:"wait,omitempty"`
}

// Pong is the ping result.
type Pong struct {
	Version      string          `json:"version"`
	Protocol     int             `json:"protocol"`
	Capabilities json.RawMessage `json:"capabilities,omitempty"`
}
