// This file is part of arduino-flasher-cli.
//
// SPDX-FileCopyrightText: Arduino s.r.l. and/or its affiliated companies
// SPDX-License-Identifier: GPL-3.0-or-later

package golden

import (
	"fmt"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/arduino/arduino-flasher-cli/internal/golden"
	"github.com/spf13/cobra"
)

type progressState struct {
	phase                 string
	printedAt, changedAt  time.Duration
	printedDone, seenDone uint64
	printed               bool
}
type progressLog struct {
	cmd     *cobra.Command
	mu      *sync.Mutex
	verbose bool
	states  map[string]*progressState
	timings map[string]map[string]time.Duration
}

func newProgressLog(cmd *cobra.Command, mu *sync.Mutex, verbose bool) *progressLog {
	return &progressLog{cmd: cmd, mu: mu, verbose: verbose, states: map[string]*progressState{}, timings: map[string]map[string]time.Duration{}}
}
func progressLogger(cmd *cobra.Command, mu *sync.Mutex) func(golden.Progress) {
	return newProgressLog(cmd, mu, false).update
}
func (l *progressLog) update(p golden.Progress) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.states[p.Serial]
	fresh := s == nil || s.phase != p.Phase
	if fresh {
		s = &progressState{phase: p.Phase}
		l.states[p.Serial] = s
	}
	if p.Done > s.seenDone {
		s.changedAt = p.Elapsed
		if p.SampleElapsed > 0 {
			s.changedAt = p.SampleElapsed
		}
		s.seenDone = p.Done
	}
	if p.Finished {
		if l.timings[p.Serial] == nil {
			l.timings[p.Serial] = map[string]time.Duration{}
		}
		l.timings[p.Serial][p.Phase] = p.Elapsed
	}
	quiet := p.Phase == "probe" || p.Phase == "queued" || p.Phase == "verify checksums"
	print := p.Failed
	if l.verbose {
		print = print || fresh || p.Finished || p.Elapsed-s.printedAt >= 5*time.Second
	} else if !quiet {
		advanced := p.Done > s.printedDone
		milestone := p.Total > 0 && p.Done >= s.printedDone && float64(p.Done-s.printedDone)/float64(p.Total) >= 0.05
		print = print || fresh || p.Finished || (advanced && (milestone || p.Elapsed-s.printedAt >= 30*time.Second) && p.Elapsed-s.printedAt >= 5*time.Second) || p.Elapsed-s.printedAt >= 60*time.Second
	} else if !p.Finished && p.Elapsed >= 30*time.Second {
		print = print || !s.printed || p.Elapsed-s.printedAt >= 60*time.Second
	}
	if !print {
		return
	}
	line := formatProgress(p)
	if !fresh && !p.Finished && p.Done == s.printedDone {
		line = formatHeartbeat(p, p.Elapsed-s.changedAt)
	}
	fmt.Fprintln(l.cmd.ErrOrStderr(), line)
	s.printedAt, s.printedDone, s.printed = p.Elapsed, p.Done, true
}
func progressLabel(p golden.Progress) string {
	if p.Serial == "" {
		return "image"
	}
	return "0x" + p.Serial
}
func isWrite(p golden.Progress) bool { return strings.HasPrefix(p.Phase, "write") }
func progressBytes(n uint64) string {
	if n >= 1<<30 {
		return fmt.Sprintf("%.2f GiB", float64(n)/(1<<30))
	}
	if n >= 1<<20 {
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	}
	if n >= 1<<10 {
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
func formatProgress(p golden.Progress) string {
	if p.Phase == "comparison result" {
		if p.PlannedWrite == 0 {
			return fmt.Sprintf("[%s] identical: %s verified by pre-read; no writes needed", progressLabel(p), progressBytes(p.Total))
		}
		saved := float64(0)
		if p.Total > 0 {
			saved = 100 * float64(p.PlannedSkip) / float64(p.Total)
		}
		return fmt.Sprintf("[%s] comparison: write %s; skip %s (%.1f%%); full readback follows", progressLabel(p), progressBytes(p.PlannedWrite), progressBytes(p.PlannedSkip), saved)
	}
	phase := p.Phase
	if isWrite(p) {
		phase = "write"
	}
	prefix := fmt.Sprintf("[%s] %s", progressLabel(p), phase)
	if p.Phase == "queued" {
		return prefix + " (waiting for worker)"
	}
	if p.Failed {
		return fmt.Sprintf("%s FAILED at %s/%s after %s", prefix, progressBytes(p.Done), progressBytes(p.Total), p.Elapsed.Round(time.Second))
	}
	if p.Finished {
		line := fmt.Sprintf("%s done: %s in %s", prefix, progressBytes(p.Done), p.Elapsed.Round(time.Second))
		if p.Done >= 1<<20 && p.Elapsed > 0 {
			line += fmt.Sprintf(" (%.1f MiB/s)", float64(p.Done)/(1<<20)/p.Elapsed.Seconds())
		}
		return line
	}
	if p.Done == 0 {
		line := prefix + " started"
		if p.Total > 0 {
			line += " (" + progressBytes(p.Total) + ")"
		}
		if p.TotalChunks > 0 {
			line += fmt.Sprintf("; %d chunks, progress on acknowledgement", p.TotalChunks)
		}
		return line
	}
	done := min(p.Done, p.Total)
	line := prefix
	if p.Total > 0 {
		line += fmt.Sprintf(" %5.1f%% | %s/%s", 100*float64(done)/float64(p.Total), progressBytes(done), progressBytes(p.Total))
	}
	if p.TotalChunks > 0 {
		line += fmt.Sprintf(" | chunk %d/%d", p.CompletedChunks, p.TotalChunks)
	}
	line += fmt.Sprintf(" | %s", p.Elapsed.Round(time.Second))
	sample := p.Elapsed
	if isWrite(p) {
		sample = p.SampleElapsed
	}
	if sample > 0 && p.Done > 0 {
		rate := float64(p.Done) / sample.Seconds()
		line += fmt.Sprintf(" | %.1f MiB/s", rate/(1<<20))
		if p.Done < p.Total {
			remaining := float64(p.Total-p.Done) / rate
			if remaining < 365*24*3600 {
				line += fmt.Sprintf(" | phase ETA ~%s", max(time.Second, time.Duration(remaining*float64(time.Second))).Round(time.Second))
			}
		}
	}
	if p.Total > 0 && p.Done >= p.Total {
		line += " | finalizing"
	}
	return line
}
func formatHeartbeat(p golden.Progress, age time.Duration) string {
	prefix := fmt.Sprintf("[%s] %s", progressLabel(p), p.Phase)
	if isWrite(p) {
		if p.CompletedChunks == 0 && p.TotalChunks > 0 {
			return fmt.Sprintf("[%s] write: awaiting first chunk acknowledgement | elapsed %s", progressLabel(p), p.Elapsed.Round(time.Second))
		}
		if p.CompletedChunks < p.TotalChunks {
			return fmt.Sprintf("[%s] write: awaiting chunk %d/%d acknowledgement | %s since last ack | elapsed %s", progressLabel(p), p.CompletedChunks+1, p.TotalChunks, age.Round(time.Second), p.Elapsed.Round(time.Second))
		}
		return fmt.Sprintf("[%s] write: finalizing | elapsed %s", progressLabel(p), p.Elapsed.Round(time.Second))
	}
	return fmt.Sprintf("%s: no new progress for %s | elapsed %s", prefix, age.Round(time.Second), p.Elapsed.Round(time.Second))
}
func (l *progressLog) summary(results []golden.Result) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(results) == 0 {
		return
	}
	w := tabwriter.NewWriter(l.cmd.ErrOrStderr(), 0, 0, 2, ' ', 0)
	hasComparison := false
	for _, values := range l.timings {
		if _, ok := values["compare contents"]; ok {
			hasComparison = true
		}
	}
	header := "SERIAL\tSTATUS\tWRITE\tREADBACK\tCHECKSUM"
	if hasComparison {
		header += "\tPRE-READ\tCOMPARE"
	}
	fmt.Fprintln(w, header+"\tTOTAL\tWRITTEN\tSKIPPED")
	for _, r := range results {
		values := l.timings[r.Serial]
		duration := func(phase string) string {
			if d, ok := values[phase]; ok {
				return d.Round(time.Second).String()
			}
			return "—"
		}
		fmt.Fprintf(w, "0x%s\t%s\t%s\t%s\t%s", r.Serial, strings.ToUpper(r.Status), duration("write"), duration("readback"), duration("verify checksums"))
		if hasComparison {
			fmt.Fprintf(w, "\t%s\t%s", duration("pre-read"), duration("compare contents"))
		}
		written, skipped := progressBytes(r.BytesWritten), progressBytes(r.BytesSkipped)
		if r.Status != "verified" {
			// A transfer failure may leave partial writes whose extent is unknown.
			if r.BytesWritten == 0 {
				written = "—"
			}
			if r.BytesSkipped == 0 {
				skipped = "—"
			}
		}
		fmt.Fprintf(w, "\t%s\t%s\t%s\n", (time.Duration(r.DurationSeconds * float64(time.Second))).Round(time.Second), written, skipped)
	}
	_ = w.Flush()
}
