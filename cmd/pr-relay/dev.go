package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/drodimin/pr-relay/internal/config"
	"github.com/drodimin/pr-relay/internal/ghclient"
	"github.com/drodimin/pr-relay/internal/model"
	"github.com/drodimin/pr-relay/internal/paths"
	"github.com/drodimin/pr-relay/internal/poller"
	"github.com/drodimin/pr-relay/internal/render"
	"github.com/drodimin/pr-relay/internal/store"
)

// envGraphQLURL redirects the GraphQL endpoint. It exists for tests only;
// ghclient.New refuses any host other than api.github.com or loopback, so it
// cannot send the token off the machine (T-02-40).
const envGraphQLURL = "PR_RELAY_GITHUB_GRAPHQL_URL"

// Identity of the dev harness's subscriptions. A restart re-subscribes with
// the same identity, which upserts and keeps pending events (SUB-04).
const (
	devSocket = "dev"
	devPane   = "dev-watch"
)

func newDevCmd() *cobra.Command {
	dev := &cobra.Command{
		Use:    "dev",
		Short:  "Developer tools",
		Hidden: true,
	}
	var sinkPath, cfgPath string
	watch := &cobra.Command{
		Use:   "watch [--sink FILE] [--config FILE] <PR-URL>...",
		Short: "Run the store and poller in the foreground for the given pull requests",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDevWatch(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), args, sinkPath, cfgPath)
		},
	}
	watch.Flags().StringVar(&sinkPath, "sink", "", "append delivered events as JSON lines to `FILE` (crash testing)")
	watch.Flags().StringVar(&cfgPath, "config", "", "read config from `FILE` instead of the default path")
	dev.AddCommand(watch)
	return dev
}

type devSub struct {
	id  string
	ref model.PRRef
	key string
}

// devPrinter drains the dev subscriptions after every commit and prints the
// rendered lines plus a one-line poll summary (D-21).
type devPrinter struct {
	mu   sync.Mutex
	out  io.Writer
	log  *slog.Logger
	eng  *poller.Engine
	sink *sink
	subs []devSub
}

// drain delivers every pending event: sink first (fsynced), then stdout,
// then Ack. A kill anywhere in between re-delivers nothing (see sink).
func (p *devPrinter) drain() {
	for _, s := range p.subs {
		d, err := p.eng.Pending(s.id)
		if err != nil {
			p.log.Error("pending", "pr", s.key, "error", err)
			continue
		}
		if len(d.Events) > 0 {
			if p.sink != nil {
				if err := p.sink.write(s.key, d.Events); err != nil {
					p.log.Error("sink write failed", "pr", s.key, "error", err)
					continue
				}
			}
			evs := make([]model.Event, len(d.Events))
			for i, pe := range d.Events {
				evs[i] = pe.Event
			}
			for _, l := range render.Lines(s.ref, evs) {
				fmt.Fprintln(p.out, l)
			}
		}
		if d.Dropped > 0 {
			fmt.Fprintf(p.out, "[pr-relay] %s note: %d updates dropped\n", s.ref, d.Dropped)
		}
		if d.UpToSeq > 0 {
			if err := p.eng.Ack(s.id, d.UpToSeq); err != nil {
				p.log.Error("ack failed", "pr", s.key, "error", err)
			}
		}
	}
}

func (p *devPrinter) startup() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.drain()
}

func (p *devPrinter) onCommit(r poller.CommitReport) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.drain()
	fmt.Fprintf(p.out, "poll prs=%d cost=%d remaining=%d/%d reset=%s forced=%t\n",
		len(r.PRs), r.Cost, r.Remaining, r.Limit, r.ResetAt.UTC().Format(time.RFC3339), r.Forced)
	for _, pr := range r.PRs {
		line := fmt.Sprintf("  %s new=%d interval=%s next=%s",
			pr.Ref, pr.NewEvents, pr.Interval, pr.NextPollAt.UTC().Format(time.RFC3339))
		if pr.Err != "" {
			line += " err=" + pr.Err
		}
		fmt.Fprintln(p.out, line)
	}
}

func runDevWatch(ctx context.Context, stdout, stderr io.Writer, args []string, sinkPath, cfgPath string) error {
	refs := make([]model.PRRef, 0, len(args))
	for _, a := range args {
		ref, err := model.ParsePRURL(a)
		if err != nil {
			return err
		}
		refs = append(refs, ref)
	}

	if cfgPath == "" {
		p, err := paths.ConfigPath()
		if err != nil {
			return err
		}
		cfgPath = p
	}
	cfg, warns, err := config.Load(cfgPath)
	for _, w := range warns {
		fmt.Fprintf(stderr, "pr-relay: config: %s\n", w)
	}
	if err != nil {
		return err
	}

	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	var sk *sink
	if sinkPath != "" {
		sk, err = openSink(sinkPath)
		if err != nil {
			return err
		}
		defer func() { _ = sk.close() }()
	}

	dir, err := paths.StateDir()
	if err != nil {
		return err
	}
	st, state, err := store.Open(dir)
	if errors.Is(err, store.ErrLocked) {
		return fmt.Errorf("another pr-relay is using %s", dir)
	}
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	client, err := ghclient.New(ghclient.Options{
		Endpoint:  os.Getenv(envGraphQLURL),
		Tokens:    &ghclient.TokenSource{},
		UserAgent: "pr-relay/" + version,
		Logger:    log,
	})
	if err != nil {
		return err
	}

	pr := &devPrinter{out: stdout, log: log, sink: sk}
	eng, err := poller.New(poller.Options{
		Saver:    st,
		State:    state,
		Fetcher:  client,
		Config:   cfg,
		Logger:   log,
		OnCommit: pr.onCommit,
	})
	if err != nil {
		return err
	}
	pr.eng = eng
	if qp, at := st.Quarantine(); qp != "" {
		log.Error("state file was unreadable and has been quarantined", "path", qp)
		eng.SetQuarantine(qp, at)
	}

	for _, ref := range refs {
		info, err := eng.Subscribe(poller.SubRequest{PR: ref, HerdrSocket: devSocket, PaneID: devPane})
		if err != nil {
			return fmt.Errorf("subscribe %s: %w", ref, err)
		}
		pr.subs = append(pr.subs, devSub{id: info.SubID, ref: ref, key: store.PRKey(ref)})
	}

	// Events written to the sink by a killed run but never acked are
	// cleared here, so they are not written or printed twice. Each
	// subscription is acked only up to its own PR's high-water mark: acking
	// with another PR's mark would discard events never written.
	if sk != nil {
		for _, s := range pr.subs {
			hw := sk.highWater(s.key)
			if hw == 0 {
				continue
			}
			if err := eng.Ack(s.id, hw); err != nil {
				return fmt.Errorf("ack %s: %w", s.ref, err)
			}
		}
	}
	pr.startup()

	if ctx == nil {
		ctx = context.Background()
	}
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return eng.Run(ctx)
}
