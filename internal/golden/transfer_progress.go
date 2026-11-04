// This file is part of arduino-flasher-cli.
//
// SPDX-FileCopyrightText: Arduino s.r.l. and/or its affiliated companies
// SPDX-License-Identifier: GPL-3.0-or-later

package golden

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type transferPhase struct {
	name        string
	ops         []operation
	total, done uint64
	next        int
	ackElapsed  time.Duration
}
type transferProgress struct {
	mu          sync.Mutex
	ctx         context.Context
	dir, serial string
	phases      []transferPhase
	current     int
	started     time.Time
	pending     []byte
}

func newTransferProgress(ctx context.Context, dir, serial string, xmlPaths []string) (*transferProgress, error) {
	t := &transferProgress{ctx: ctx, dir: dir, serial: serial, started: time.Now()}
	for _, file := range xmlPaths {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		var doc struct {
			Ops []operation `xml:",any"`
		}
		if err := xml.Unmarshal(data, &doc); err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(filepath.Base(file), ".xml")
		switch name {
		case "capture":
			name = "capture read"
		case "compare":
			name = "pre-read"
		case "restore":
			name = "write"
		case "verify":
			name = "readback"
		}
		p := transferPhase{name: name, ops: doc.Ops}
		for _, op := range p.ops {
			p.total += op.Count * op.Sector
		}
		if len(p.ops) > 0 {
			t.phases = append(t.phases, p)
		}
	}
	return t, nil
}
func (t *transferProgress) emitLocked(finished, failed bool) {
	if t.current >= len(t.phases) {
		return
	}
	p := &t.phases[t.current]
	done := p.done
	if p.next < len(p.ops) {
		op := p.ops[p.next]
		if op.XMLName.Local == "read" {
			file := op.File
			if !filepath.IsAbs(file) {
				file = filepath.Join(t.dir, file)
			}
			if st, err := os.Stat(file); err == nil && st.Size() > 0 {
				done += min(uint64(st.Size()), op.Count*op.Sector)
			}
		}
	}
	event := Progress{Serial: t.serial, Phase: p.name, Done: done, Total: p.total, Elapsed: time.Since(t.started), Finished: finished, Failed: failed}
	event.SampleElapsed = event.Elapsed
	if len(p.ops) > 0 && p.ops[0].XMLName.Local == "program" {
		event.SampleElapsed = p.ackElapsed
		event.CompletedChunks, event.TotalChunks = p.next, len(p.ops)
	}
	reportProgress(t.ctx, event)
}
func (t *transferProgress) tick() { t.mu.Lock(); defer t.mu.Unlock(); t.emitLocked(false, false) }
func (t *transferProgress) finish(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.emitLocked(true, err != nil)
}

// Write handles arbitrary pipe boundaries and keeps only a bounded partial line.
func (t *transferProgress) Write(data []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(data)
	for _, b := range data {
		if b == '\n' || b == '\r' {
			t.lineLocked(string(bytes.TrimSpace(t.pending)))
			t.pending = t.pending[:0]
		} else if len(t.pending) < 8192 {
			t.pending = append(t.pending, b)
		}
	}
	return n, nil
}
func (t *transferProgress) lineLocked(line string) {
	if t.current >= len(t.phases) {
		return
	}
	p := &t.phases[t.current]
	if p.next >= len(p.ops) {
		return
	}
	op := p.ops[p.next]
	verb, name := "read", op.File
	if op.XMLName.Local == "program" {
		verb, name = "flashed", op.Label
	}
	prefix := fmt.Sprintf("%s %q successfully", verb, name)
	if line != prefix && !strings.HasPrefix(line, prefix+" at ") {
		return
	}
	p.done += op.Count * op.Sector
	p.next++
	p.ackElapsed = time.Since(t.started)
	if p.next == len(p.ops) && t.current+1 < len(t.phases) {
		t.emitLocked(true, false)
		t.current++
		t.started = time.Now()
		t.emitLocked(false, false)
	} else if op.XMLName.Local == "program" {
		// Publish fresh measurements immediately instead of waiting for a timer.
		t.emitLocked(false, false)
	}
}
