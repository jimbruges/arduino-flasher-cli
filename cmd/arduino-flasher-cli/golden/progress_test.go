// This file is part of arduino-flasher-cli.
//
// SPDX-FileCopyrightText: Arduino s.r.l. and/or its affiliated companies
// SPDX-License-Identifier: GPL-3.0-or-later

package golden

import (
	"bytes"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arduino/arduino-flasher-cli/internal/golden"
	"github.com/stretchr/testify/require"
)

func TestProgressFormatting(t *testing.T) {
	p := golden.Progress{Serial: "12345678", Phase: "capture read", Done: 1 << 30, Total: 2 << 30, Elapsed: 10 * time.Second}
	line := formatProgress(p)
	require.Contains(t, line, "[0x12345678]")
	require.Contains(t, line, "50.0%")
	require.Contains(t, line, "1.00 GiB/2.00 GiB")
	require.Contains(t, line, "102.4 MiB/s")
	require.Contains(t, line, "phase ETA ~10s")
	p.Finished = true
	p.Failed = true
	require.Contains(t, formatProgress(p), "FAILED")
	require.NotContains(t, formatProgress(p), "ETA")
	require.NotContains(t, formatProgress(p), "VERIFIED")
	p.Finished = false
	p.Failed = false
	p.Done = 0
	p.Elapsed = 0
	require.NotContains(t, formatProgress(p), "ETA")
	require.Contains(t, formatProgress(p), "started")
}
func TestWriteRateDoesNotDecayBetweenAcknowledgements(t *testing.T) {
	p := golden.Progress{Serial: "12345678", Phase: "write", Done: 1 << 30, Total: 2 << 30, SampleElapsed: 80 * time.Second, Elapsed: 80 * time.Second, CompletedChunks: 1, TotalChunks: 2}
	a := formatProgress(p)
	p.Elapsed = 140 * time.Second
	b := formatProgress(p)
	for _, line := range []string{a, b} {
		require.Contains(t, line, "12.8 MiB/s")
		require.Contains(t, line, "phase ETA ~1m20s")
	}
	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetErr(&out)
	log := progressLogger(cmd, &sync.Mutex{})
	p.Elapsed = 80 * time.Second
	log(p)
	out.Reset()
	p.Elapsed = 100 * time.Second
	log(p)
	require.Empty(t, out.String())
	p.Elapsed = 140 * time.Second
	log(p)
	require.Contains(t, out.String(), "awaiting chunk 2/2 acknowledgement")
	require.Contains(t, out.String(), "1m0s since last ack")
	require.NotContains(t, out.String(), "ETA")
	require.NotContains(t, out.String(), "MiB/s")
}
func TestCompactAndVerboseProgress(t *testing.T) {
	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetErr(&out)
	log := progressLogger(cmd, &sync.Mutex{})
	p := golden.Progress{Serial: "12345678", Phase: "write", Total: 2 << 30, TotalChunks: 2}
	log(p)
	before := out.Len()
	for i := 5; i < 60; i += 5 {
		p.Elapsed = time.Duration(i) * time.Second
		log(p)
	}
	require.Equal(t, before, out.Len())
	p.Serial = "87654321"
	p.Elapsed = 0
	log(p)
	require.Greater(t, out.Len(), before)
	out.Reset()
	p.Serial = "12345678"
	p.Elapsed = time.Minute
	log(p)
	require.Contains(t, out.String(), "awaiting first chunk")
	out.Reset()
	p.Phase = "probe"
	p.Elapsed = 0
	log(p)
	p.Elapsed = 2 * time.Second
	p.Finished = true
	log(p)
	require.Empty(t, out.String())
	p.Failed = true
	log(p)
	require.Contains(t, out.String(), "FAILED")
	out.Reset()
	verbose := newProgressLog(cmd, &sync.Mutex{}, true)
	p = golden.Progress{Serial: "12345678", Phase: "probe"}
	verbose.update(p)
	require.Contains(t, out.String(), "probe started")
	p.Elapsed = 5 * time.Second
	verbose.update(p)
	require.Equal(t, 2, strings.Count(out.String(), "\n"))
}
func TestThreeBoardProgressReplay(t *testing.T) {
	// Reproduce the reported batch durations with 1 GiB write acknowledgements
	// and five-second reads, without USB access or wall-clock sleeps.
	const total = uint64(15675162624)
	type event struct {
		at time.Duration
		p  golden.Progress
	}
	var events []event
	var results []golden.Result
	for _, board := range []struct {
		serial      string
		write, read int
		duration    float64
	}{
		{"8ED963AB", 907, 952, 1867.656725416}, {"C2B2F977", 1196, 949, 2152.781266708}, {"4A712118", 1230, 925, 2163.163142875},
	} {
		results = append(results, golden.Result{Serial: board.serial, Status: "verified", DurationSeconds: board.duration})
		offset := 0
		for _, phase := range []struct {
			name    string
			seconds int
		}{{"probe", 2}, {"write", board.write}, {"readback", board.read}, {"verify checksums", 6}} {
			for sec := 0; sec < phase.seconds; sec += 5 {
				p := golden.Progress{Serial: board.serial, Phase: phase.name, Total: total, Elapsed: time.Duration(sec) * time.Second}
				p.Done = uint64(float64(total) * float64(sec) / float64(phase.seconds))
				if phase.name == "write" {
					p.CompletedChunks = int(p.Done / (1 << 30))
					p.TotalChunks = 15
					p.Done = uint64(p.CompletedChunks) * (1 << 30)
					p.SampleElapsed = time.Duration(float64(p.Done) / float64(total) * float64(phase.seconds) * float64(time.Second))
				}
				events = append(events, event{time.Duration(offset+sec) * time.Second, p})
			}
			p := golden.Progress{Serial: board.serial, Phase: phase.name, Done: total, Total: total, Elapsed: time.Duration(phase.seconds) * time.Second, Finished: true}
			events = append(events, event{time.Duration(offset+phase.seconds) * time.Second, p})
			offset += phase.seconds
		}
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].at < events[j].at })
	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetErr(&out)
	log := newProgressLog(cmd, &sync.Mutex{}, false)
	for _, e := range events {
		log.update(e.p)
	}
	lines := strings.Count(out.String(), "\n")
	require.Less(t, lines, len(events)/4, "compact output should remove at least 75%% of frequent updates")
	require.NotContains(t, out.String(), "0.0 MiB/s")
	out.Reset()
	log.summary(results)
	require.Contains(t, out.String(), "READBACK")
	require.Contains(t, out.String(), "15m7s")
	require.Contains(t, out.String(), "15m52s")
	require.Contains(t, out.String(), "31m8s")
	for _, r := range results {
		require.Contains(t, out.String(), r.Serial)
	}
	t.Logf("three-board replay: %d events -> %d progress lines", len(events), lines)
}
func TestSummaryIncludesFailuresAndMissingPhases(t *testing.T) {
	cmd := NewCommand()
	var out, stdout bytes.Buffer
	cmd.SetErr(&out)
	cmd.SetOut(&stdout)
	log := newProgressLog(cmd, &sync.Mutex{}, false)
	log.update(golden.Progress{Serial: "12345678", Phase: "write", Elapsed: 10 * time.Second, Finished: true, Failed: true})
	out.Reset()
	log.summary([]golden.Result{{Serial: "12345678", Status: "failed", DurationSeconds: 10}, {Serial: "87654321", Status: "verified", DurationSeconds: 20}})
	require.Contains(t, out.String(), "FAILED")
	require.Contains(t, out.String(), "10s")
	require.Contains(t, out.String(), "—")
	require.Empty(t, stdout.String())
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.Contains(line, "12345678") {
			require.NotContains(t, line, "0 B", "partial writes must not be reported as zero")
		}
		if strings.Contains(line, "87654321") {
			require.Contains(t, line, "0 B", "verified unchanged targets have known zero writes")
		}
	}
}

func TestComparisonResultExplainsSkippedWrites(t *testing.T) {
	p := golden.Progress{Serial: "C2B2F977", Phase: "comparison result", Total: 15675162624, PlannedSkip: 15675162624}
	line := formatProgress(p)
	require.Contains(t, line, "identical")
	require.Contains(t, line, "verified by pre-read")
	require.Contains(t, line, "no writes needed")
	p.PlannedWrite = 106954752
	p.PlannedSkip = p.Total - p.PlannedWrite
	line = formatProgress(p)
	require.Contains(t, line, "write 102.0 MiB")
	require.Contains(t, line, "99.3%")
	require.Contains(t, line, "full readback follows")
	require.NotContains(t, line, "identical")
}
