// This file is part of arduino-flasher-cli.
//
// SPDX-FileCopyrightText: Arduino s.r.l. and/or its affiliated companies
// SPDX-License-Identifier: GPL-3.0-or-later

package golden

import (
	"context"
	"time"
)

// Progress describes one phase, not overall board readiness. Write counts are
// acknowledged QDL chunks; read counts are bytes received into output files.
// Callbacks can run concurrently for different targets and must return promptly.
type Progress struct {
	Serial, Phase string
	Done, Total   uint64
	Elapsed       time.Duration
	// SampleElapsed is the time of the last byte/chunk measurement. It stays
	// fixed between write acknowledgements; Elapsed continues advancing.
	SampleElapsed                time.Duration
	CompletedChunks, TotalChunks int
	Finished, Failed             bool
	// Comparison results describe planned writes after the full pre-read.
	PlannedWrite, PlannedSkip uint64
}
type progressKey struct{}
type counterKey struct{}
type progressCounter struct {
	ctx           context.Context
	event         Progress
	started, last time.Time
}

func WithProgress(ctx context.Context, report func(Progress)) context.Context {
	return context.WithValue(ctx, progressKey{}, report)
}
func reportProgress(ctx context.Context, p Progress) {
	if report, ok := ctx.Value(progressKey{}).(func(Progress)); ok && report != nil {
		report(p)
	}
}
func beginProgress(ctx context.Context, serial, phase string, total uint64) (context.Context, *progressCounter) {
	now := time.Now()
	p := &progressCounter{ctx: ctx, event: Progress{Serial: serial, Phase: phase, Total: total}, started: now, last: now}
	reportProgress(ctx, p.event)
	return context.WithValue(ctx, counterKey{}, p), p
}
func advanceProgress(ctx context.Context, n uint64) {
	if p, ok := ctx.Value(counterKey{}).(*progressCounter); ok {
		p.event.Done += n
		if time.Since(p.last) >= 5*time.Second {
			p.emit(false, false)
		}
	}
}
func (p *progressCounter) emit(finished, failed bool) {
	p.last = time.Now()
	p.event.Elapsed = p.last.Sub(p.started)
	p.event.Finished, p.event.Failed = finished, failed
	reportProgress(p.ctx, p.event)
}
func (p *progressCounter) finish(err error) { p.emit(true, err != nil) }
func manifestBytes(m Manifest) uint64 {
	var total uint64
	for _, l := range m.LUNs {
		total += l.Sectors * l.SectorSize
	}
	return total
}
