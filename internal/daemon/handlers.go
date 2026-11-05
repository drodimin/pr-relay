package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/drodimin/pr-relay/internal/ctl"
	"github.com/drodimin/pr-relay/internal/ghclient"
	"github.com/drodimin/pr-relay/internal/ghquery"
	"github.com/drodimin/pr-relay/internal/herdr"
	"github.com/drodimin/pr-relay/internal/model"
	"github.com/drodimin/pr-relay/internal/poller"
	"github.com/drodimin/pr-relay/internal/store"
)

// Handler time bounds.
const (
	// paneGetTimeout bounds the herdr identity capture in sub.
	paneGetTimeout = 1500 * time.Millisecond
	// verifyTimeout bounds the GitHub probe in sub (D-08).
	verifyTimeout = 5 * time.Second
)

// handlers returns the control method handlers. hello and shutdown are
// built into ctl.Server.
func (h *host) handlers() map[string]ctl.Handler {
	return map[string]ctl.Handler{
		ctl.MethodSub:    h.sub,
		ctl.MethodUnsub:  h.unsub,
		ctl.MethodKeep:   h.keep,
		ctl.MethodPoll:   h.poll,
		ctl.MethodLs:     h.ls,
		ctl.MethodStatus: h.status,
		ctl.MethodReset:  h.reset,
	}
}

func badRequest(format string, a ...any) *ctl.Error {
	return &ctl.Error{Code: ctl.CodeBadRequest, Message: fmt.Sprintf(format, a...)}
}

// decode strictly decodes params into v (T-04-40). Empty params decode as
// the zero value.
func decode(params json.RawMessage, v any) error {
	if len(bytes.TrimSpace(params)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(params))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return badRequest("invalid params: %v", err)
	}
	return nil
}

// parseLink re-validates a PR link daemon-side (DMN-08, D-06).
func parseLink(s string) (model.PRRef, error) {
	ref, err := model.ParsePRLink(s)
	if err != nil {
		return model.PRRef{}, badRequest("%s", err.Error())
	}
	return ref, nil
}

// target validates and cleans a delivery target: an absolute herdr socket
// path and a non-empty pane ID (T-04-39).
func target(t ctl.Target) (ctl.Target, error) {
	if t.PaneID == "" {
		return t, badRequest("pane id is required (run inside a herdr pane)")
	}
	if t.HerdrSocket == "" || !filepath.IsAbs(t.HerdrSocket) {
		return t, badRequest("herdr socket must be an absolute path, got %q", t.HerdrSocket)
	}
	t.HerdrSocket = filepath.Clean(t.HerdrSocket)
	return t, nil
}

// done logs one handled call: method, result code and the PR and pane keys
// only, never PR text or tokens (T-04-42).
func (h *host) done(method, pr, pane string, err error) {
	code := "ok"
	if err != nil {
		code = ctl.Code(err)
		if code == "" {
			code = ctl.CodeInternal
		}
	}
	h.log.Info("ctl call", "method", method, "code", code, "pr", pr, "pane", pane)
}

func eventStrings(ts []model.EventType) []string {
	if len(ts) == 0 {
		return nil
	}
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = string(t)
	}
	return out
}

// validEvents converts and checks an --events list.
func validEvents(in []string) ([]model.EventType, error) {
	if len(in) == 0 {
		return nil, nil
	}
	known := map[model.EventType]bool{}
	for _, t := range model.AllEventTypes() {
		known[t] = true
	}
	out := make([]model.EventType, 0, len(in))
	for _, s := range in {
		t := model.EventType(s)
		if !known[t] {
			return nil, badRequest("unknown event type %q (valid: %s)", s, eventList())
		}
		out = append(out, t)
	}
	return out, nil
}

func eventList() string {
	var b bytes.Buffer
	for i, t := range model.AllEventTypes() {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(string(t))
	}
	return b.String()
}

func (h *host) sub(ctx context.Context, params json.RawMessage) (res any, err error) {
	var p ctl.SubParams
	var prKey, pane string
	defer func() { h.done(ctl.MethodSub, prKey, pane, err) }()
	if err := decode(params, &p); err != nil {
		return nil, err
	}
	pane = p.PaneID
	ref, err := parseLink(p.URL)
	if err != nil {
		return nil, err
	}
	prKey = store.PRKey(ref)
	t, err := target(p.Target)
	if err != nil {
		return nil, err
	}
	events, err := validEvents(p.Events)
	if err != nil {
		return nil, err
	}

	// Identity capture (D-07, D-10). Nothing is stored when herdr cannot
	// confirm the pane.
	hc, err := herdr.NewClient(herdr.Options{Socket: t.HerdrSocket, Logger: h.log})
	if err != nil {
		return nil, badRequest("%v", err)
	}
	pctx, cancel := context.WithTimeout(ctx, paneGetTimeout)
	info, err := hc.PaneGet(pctx, t.PaneID)
	cancel()
	switch {
	case err == nil:
	case herdr.Code(err) == herdr.CodePaneNotFound:
		return nil, &ctl.Error{Code: ctl.CodePaneNotFound, Message: fmt.Sprintf("pane %s not found in herdr at %s", t.PaneID, t.HerdrSocket)}
	case herdr.IsUnreachable(err):
		return nil, &ctl.Error{Code: ctl.CodeHerdrUnreachable, Message: fmt.Sprintf("herdr not reachable at %s", t.HerdrSocket)}
	default:
		return nil, &ctl.Error{Code: ctl.CodeHerdrUnreachable, Message: fmt.Sprintf("herdr at %s: %v", t.HerdrSocket, err)}
	}
	warning := ""
	if info.Agent == "" {
		warning = fmt.Sprintf("no agent detected in pane %s; updates are delivered only while an agent runs in the pane", t.PaneID)
	}
	session := ""
	if info.AgentSession != nil {
		session = info.AgentSession.Value
	}

	vctx, vcancel := context.WithTimeout(ctx, verifyTimeout)
	defer vcancel()
	si, err := h.eng.SubscribeVerified(vctx, poller.SubRequest{
		PR:           ref,
		HerdrSocket:  t.HerdrSocket,
		PaneID:       t.PaneID,
		AgentSession: session,
		AgentKind:    info.Agent,
		TerminalID:   info.TerminalID,
		Events:       events,
	})
	if err != nil {
		return nil, verifyError(ref, err)
	}
	out := ctl.SubResult{
		PR:         ref.String(),
		URL:        ref.URL(),
		Events:     eventStrings(events),
		Created:    si.Created,
		Interval:   si.Interval,
		NextPollAt: si.NextPollAt,
		Warning:    warning,
	}
	if len(events) > 0 {
		// Report the stored, normalised mask.
		for _, s := range h.eng.List() {
			if s.ID == si.SubID {
				out.Events = eventStrings(s.Events)
			}
		}
	}
	for _, c := range si.CoSubscribers {
		if filepath.Clean(c.HerdrSocket) == t.HerdrSocket {
			out.CoSubscribers = append(out.CoSubscribers, c.PaneID)
		} else {
			out.CoSubscribers = append(out.CoSubscribers, c.HerdrSocket+":"+c.PaneID)
		}
	}
	return out, nil
}

// verifyError maps a SubscribeVerified failure to a wire error.
func verifyError(ref model.PRRef, err error) error {
	var ve *poller.VerifyError
	if !errors.As(err, &ve) {
		if errors.Is(err, context.DeadlineExceeded) {
			return &ctl.Error{Code: ctl.CodeTimeout, Message: "GitHub did not answer within 5s; retry later"}
		}
		return err
	}
	return classError(ref, ve.Class)
}

// classError turns a poller error class into a wire error with a message
// the agent can act on.
func classError(ref model.PRRef, class string) *ctl.Error {
	switch class {
	case "not_found":
		return &ctl.Error{Code: ctl.CodeNotFound, Message: fmt.Sprintf("%s not found, or not visible with your gh credentials", ref)}
	case "forbidden":
		return &ctl.Error{Code: ctl.CodeForbidden, Message: fmt.Sprintf("%s: access forbidden with your gh credentials", ref)}
	case "unauthorized":
		return &ctl.Error{Code: ctl.CodeUnauthorized, Message: "GitHub rejected the token; run `gh auth login` and retry"}
	case "no_token":
		return &ctl.Error{Code: ctl.CodeNoToken, Message: "no GitHub token found; run `gh auth login` and retry"}
	case "rate_limited":
		return &ctl.Error{Code: ctl.CodeRateLimited, Message: "GitHub rate limit reached; retry later"}
	case "secondary_limit":
		return &ctl.Error{Code: ctl.CodeSecondaryLimit, Message: "GitHub rate limit reached; retry later"}
	case poller.ClassTimeout:
		return &ctl.Error{Code: ctl.CodeTimeout, Message: "GitHub did not answer within 5s; retry later"}
	case poller.ClassPRClosed:
		return &ctl.Error{Code: ctl.CodePRClosed, Message: fmt.Sprintf("%s is already merged or closed; nothing to watch", ref)}
	default:
		return &ctl.Error{Code: ctl.CodeUnavailable, Message: "GitHub request failed; retry later"}
	}
}

// pollErrClass classifies a per-PR poll error the same way the poller's
// LastError does.
func pollErrClass(err error) string {
	var se *ghclient.SecondaryLimitError
	var pe *ghclient.PrimaryExhaustedError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return poller.ClassTimeout
	case errors.Is(err, ghquery.ErrNotFound):
		return "not_found"
	case errors.Is(err, ghquery.ErrForbidden), errors.Is(err, ghclient.ErrForbidden):
		return "forbidden"
	case errors.Is(err, ghclient.ErrUnauthorized):
		return "unauthorized"
	case errors.Is(err, ghclient.ErrNoToken):
		return "no_token"
	case errors.As(err, &se):
		return "secondary_limit"
	case errors.As(err, &pe):
		return "rate_limited"
	}
	return "transient"
}

func (h *host) unsub(_ context.Context, params json.RawMessage) (res any, err error) {
	var p ctl.UnsubParams
	var prKey, pane string
	defer func() { h.done(ctl.MethodUnsub, prKey, pane, err) }()
	if err := decode(params, &p); err != nil {
		return nil, err
	}
	pane = p.PaneID
	t, err := target(p.Target)
	if err != nil {
		return nil, err
	}
	var only *model.PRRef
	switch {
	case p.All && p.URL != "":
		return nil, badRequest("pass either a pull request URL or --all, not both")
	case p.All:
	case p.URL == "":
		return nil, badRequest("pass a pull request URL, or --all for every subscription of this pane")
	default:
		ref, err := parseLink(p.URL)
		if err != nil {
			return nil, err
		}
		prKey = store.PRKey(ref)
		only = &ref
	}
	n, err := h.eng.UnsubscribePane(t.HerdrSocket, t.PaneID, only)
	if err != nil {
		return nil, err
	}
	return ctl.UnsubResult{Removed: n}, nil
}

func (h *host) keep(_ context.Context, params json.RawMessage) (res any, err error) {
	var p ctl.KeepParams
	var prKey, pane string
	defer func() { h.done(ctl.MethodKeep, prKey, pane, err) }()
	if err := decode(params, &p); err != nil {
		return nil, err
	}
	pane = p.PaneID
	ref, err := parseLink(p.URL)
	if err != nil {
		return nil, err
	}
	prKey = store.PRKey(ref)
	t, err := target(p.Target)
	if err != nil {
		return nil, err
	}
	id := store.SubID(t.HerdrSocket, t.PaneID, prKey)
	switch err := h.eng.Keep(id); {
	case errors.Is(err, poller.ErrUnknownSub):
		return nil, &ctl.Error{Code: ctl.CodeNotWatched, Message: fmt.Sprintf("not subscribed to %s from pane %s", ref, t.PaneID)}
	case errors.Is(err, poller.ErrEnded):
		return nil, &ctl.Error{Code: ctl.CodeEnded, Message: fmt.Sprintf("subscription has ended; re-subscribe with pr-relay sub %s", ref.URL())}
	case err != nil:
		return nil, err
	}
	out := ctl.KeepResult{PR: ref.String()}
	for _, s := range h.eng.List() {
		if s.ID == id {
			out.CheckinDue, out.ExpiresAt = h.eng.LifecycleDue(s)
		}
	}
	return out, nil
}

func (h *host) poll(ctx context.Context, params json.RawMessage) (res any, err error) {
	var p ctl.PollParams
	var prKey string
	defer func() { h.done(ctl.MethodPoll, prKey, "", err) }()
	if err := decode(params, &p); err != nil {
		return nil, err
	}
	ref, err := parseLink(p.URL)
	if err != nil {
		return nil, err
	}
	prKey = store.PRKey(ref)
	// PollNow moves RateAnchor to now itself, which resets the cadence
	// (D-09, D-15).
	r, err := h.eng.PollNow(ctx, ref)
	var rl *poller.RateLimitedError
	switch {
	case errors.Is(err, poller.ErrNotWatched):
		return nil, &ctl.Error{Code: ctl.CodeNotWatched, Message: fmt.Sprintf("%s is not watched; subscribe with pr-relay sub %s", ref, ref.URL())}
	case errors.As(err, &rl):
		return nil, &ctl.Error{Code: ctl.CodeRateLimited, Message: fmt.Sprintf("GitHub rate limit reached; retry after %s", rl.Until.UTC().Format(time.RFC3339))}
	case err != nil:
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, &ctl.Error{Code: ctl.CodeTimeout, Message: "GitHub did not answer in time; retry later"}
		}
		return nil, classError(ref, pollErrClass(err))
	case r.Err != nil:
		return nil, classError(ref, pollErrClass(r.Err))
	}
	out := ctl.PollResult{PR: ref.String(), NewEvents: r.NewEvents}
	out.Interval, out.NextPollAt = h.eng.Schedule(ref)
	return out, nil
}

func (h *host) ls(_ context.Context, params json.RawMessage) (res any, err error) {
	var p ctl.LsParams
	defer func() { h.done(ctl.MethodLs, "", p.PaneID, err) }()
	if err := decode(params, &p); err != nil {
		return nil, err
	}
	filter := !p.All && p.PaneID != ""
	sock := ""
	if filter {
		if p.HerdrSocket != "" {
			if !filepath.IsAbs(p.HerdrSocket) {
				return nil, badRequest("herdr socket must be an absolute path, got %q", p.HerdrSocket)
			}
			sock = filepath.Clean(p.HerdrSocket)
		}
	}
	rows := []ctl.LsRow{}
	for _, s := range h.eng.List() {
		if filter && (s.PaneID != p.PaneID || (sock != "" && filepath.Clean(s.HerdrSocket) != sock)) {
			continue
		}
		row := ctl.LsRow{
			PR:            s.PR,
			HerdrSocket:   s.HerdrSocket,
			PaneID:        s.PaneID,
			Events:        eventStrings(s.Events),
			LastEventAt:   s.LastEventAt,
			LastEventType: string(s.LastEventType),
			Ending:        s.Ending,
		}
		if v, ok := h.eng.PRView(s.PR); ok {
			row.PR, row.URL = v.Ref.String(), v.Ref.URL()
			row.Interval, _ = h.eng.Schedule(v.Ref)
		}
		row.CheckinDue, row.ExpiresAt = h.eng.LifecycleDue(s)
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].PaneID != rows[j].PaneID {
			return rows[i].PaneID < rows[j].PaneID
		}
		if rows[i].PR != rows[j].PR {
			return rows[i].PR < rows[j].PR
		}
		return rows[i].HerdrSocket < rows[j].HerdrSocket
	})
	return ctl.LsResult{Now: h.now(), Rows: rows}, nil
}

func (h *host) status(_ context.Context, params json.RawMessage) (res any, err error) {
	defer func() { h.done(ctl.MethodStatus, "", "", err) }()
	if err := decode(params, &struct{}{}); err != nil {
		return nil, err
	}
	st := h.eng.Status()
	out := ctl.StatusResult{
		Version:         h.version,
		PID:             os.Getpid(),
		StartedAt:       h.startedAt,
		Now:             h.now(),
		Socket:          h.socket,
		StateDir:        h.stateDir,
		LogPath:         h.logPath,
		AuthState:       st.Auth.State,
		AuthSource:      st.Auth.Source,
		Limit:           st.Limit,
		Remaining:       st.Remaining,
		ResetAt:         st.ResetAt,
		SpentLastHour:   st.SpentLastHour,
		Stretch:         st.Stretch,
		SecondaryUntil:  st.SecondaryUntil,
		PRs:             st.PRs,
		Subs:            st.Subs,
		QuarantinedPath: st.QuarantinedPath,
		PRErrors:        st.PRErrors,
	}
	if out.AuthSource == "" {
		out.AuthSource = h.authSource
	}
	// Cancellations surface only here, never in a pane (D-04, D-23).
	cs := h.eng.RecentCancellations()
	for i := len(cs) - 1; i >= 0; i-- {
		c := cs[i]
		out.Cancellations = append(out.Cancellations, ctl.CancelRow{PR: c.PR, PaneID: c.PaneID, Reason: c.Reason, At: c.At})
	}
	pending := map[string]int{}
	for _, s := range h.eng.List() {
		pending[filepath.Clean(s.HerdrSocket)] += len(s.Pending)
	}
	for _, sk := range h.mgr.Sockets() {
		out.Herdr = append(out.Herdr, ctl.SocketRow{Socket: sk.Socket, Up: sk.Up, Since: sk.Since, Pending: pending[filepath.Clean(sk.Socket)]})
	}
	return out, nil
}

func (h *host) reset(_ context.Context, params json.RawMessage) (res any, err error) {
	defer func() { h.done(ctl.MethodReset, "", "", err) }()
	if err := decode(params, &struct{}{}); err != nil {
		return nil, err
	}
	n, err := h.eng.Reset()
	if err != nil {
		return nil, err
	}
	return ctl.ResetResult{Removed: n}, nil
}
