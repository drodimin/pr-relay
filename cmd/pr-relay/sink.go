package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/drodimin/pr-relay/internal/store"
)

// sinkLine is one delivered event in the sink file.
type sinkLine struct {
	Seq  uint64 `json:"seq"`
	Key  string `json:"key"`
	Type string `json:"type"`
	PR   string `json:"pr"`
}

// sink is the dev harness's idempotent delivery target, used by the crash
// test (D-22). Each event is appended as one JSON line and fsynced before the
// engine is acked, so a kill between the two leaves the event both in the
// sink and pending. The next run reads the highest seq already in the file,
// acks everything up to it and skips any event at or below it: an event is
// written exactly once whatever the kill point.
type sink struct {
	f      *os.File
	maxSeq uint64
}

// openSink opens (creating, mode 0600) the sink at path and finds the highest
// seq already written. A torn last line from a killed write is cut off so the
// next append starts on a fresh line.
func openSink(path string) (*sink, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open sink: %w", err)
	}
	s := &sink{f: f}
	if err := s.scan(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return s, nil
}

func (s *sink) scan() error {
	if _, err := s.f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("sink: seek: %w", err)
	}
	data, err := io.ReadAll(s.f)
	if err != nil {
		return fmt.Errorf("sink: read: %w", err)
	}
	complete := data
	if i := bytes.LastIndexByte(data, '\n'); i < len(data)-1 {
		// Torn tail (or a file with no newline at all): drop it.
		complete = data[:i+1]
		if err := s.f.Truncate(int64(len(complete))); err != nil {
			return fmt.Errorf("sink: truncate torn line: %w", err)
		}
		if err := s.f.Sync(); err != nil {
			return fmt.Errorf("sink: sync: %w", err)
		}
	}
	sc := bufio.NewScanner(bytes.NewReader(complete))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var l sinkLine
		if err := json.Unmarshal(line, &l); err != nil {
			return fmt.Errorf("sink: malformed line: %w", err)
		}
		s.maxSeq = max(s.maxSeq, l.Seq)
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("sink: scan: %w", err)
	}
	return nil
}

// write appends every event with a seq above the sink's high-water mark, in
// one write, and fsyncs before returning.
func (s *sink) write(prKey string, evs []store.PendingEvent) error {
	var buf bytes.Buffer
	top := s.maxSeq
	for _, ev := range evs {
		if ev.Seq <= s.maxSeq {
			continue
		}
		b, err := json.Marshal(sinkLine{Seq: ev.Seq, Key: ev.Event.Key, Type: string(ev.Event.Type), PR: prKey})
		if err != nil {
			return fmt.Errorf("sink: encode: %w", err)
		}
		buf.Write(b)
		buf.WriteByte('\n')
		top = max(top, ev.Seq)
	}
	if buf.Len() == 0 {
		return nil
	}
	if _, err := s.f.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("sink: write: %w", err)
	}
	if err := s.f.Sync(); err != nil {
		return fmt.Errorf("sink: sync: %w", err)
	}
	s.maxSeq = top
	return nil
}

func (s *sink) close() error { return s.f.Close() }
