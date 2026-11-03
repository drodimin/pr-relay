package main

import (
	"path/filepath"
	"testing"

	"github.com/drodimin/pr-relay/internal/model"
	"github.com/drodimin/pr-relay/internal/store"
)

func pend(seq uint64, key string) store.PendingEvent {
	return store.PendingEvent{Seq: seq, Event: model.Event{Type: model.EventComment, Key: key}}
}

// TestSinkPerPRHighWater covers review CR-01: seqs are global but each PR's
// queue is drained separately, so writing PR B's higher seqs first must not
// make the sink skip PR A's lower ones, and a reopened sink must report each
// PR's own high-water mark.
func TestSinkPerPRHighWater(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sink.jsonl")
	s, err := openSink(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.write("o/r#2", []store.PendingEvent{pend(3, "comment:b1"), pend(4, "comment:b2")}); err != nil {
		t.Fatal(err)
	}
	if err := s.write("o/r#1", []store.PendingEvent{pend(1, "comment:a1"), pend(2, "comment:a2")}); err != nil {
		t.Fatal(err)
	}
	// A re-drain of already-written events writes nothing new.
	if err := s.write("o/r#1", []store.PendingEvent{pend(1, "comment:a1"), pend(2, "comment:a2")}); err != nil {
		t.Fatal(err)
	}
	if err := s.close(); err != nil {
		t.Fatal(err)
	}

	s, err = openSink(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.close() }()
	if a, b := s.highWater("o/r#1"), s.highWater("o/r#2"); a != 2 || b != 4 {
		t.Fatalf("high-water marks a=%d b=%d, want 2 and 4", a, b)
	}
	if hw := s.highWater("o/r#3"); hw != 0 {
		t.Errorf("unknown PR high-water = %d, want 0", hw)
	}
}
