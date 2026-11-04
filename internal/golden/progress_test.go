// This file is part of arduino-flasher-cli.
//
// SPDX-FileCopyrightText: Arduino s.r.l. and/or its affiliated companies
// SPDX-License-Identifier: GPL-3.0-or-later

package golden

import (
	"context"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCommandRunnerProgressAndDiagnostics(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake QDL executable uses a POSIX shell")
	}
	for _, fail := range []bool{false, true} {
		dir := t.TempDir()
		xmlFile, err := writeXML(dir, "capture.xml", []operation{{XMLName: xml.Name{Local: "read"}, File: "part.bin", Count: 4, Sector: 1}})
		require.NoError(t, err)
		script := "#!/bin/sh\nprintf abcd > part.bin\nprintf 'read \"part.bin\" successfully\\n'\n"
		if fail {
			script += "printf 'simulated USB failure\\n' >&2\nexit 1\n"
		}
		fakeQDL := filepath.Join(dir, "qdl")
		require.NoError(t, os.WriteFile(fakeQDL, []byte(script), 0700))
		var mu sync.Mutex
		var events []Progress
		ctx := WithProgress(context.Background(), func(p Progress) {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, p)
		})
		err = CommandRunner(fakeQDL)(ctx, dir, "12345678", "emmc", "programmer.elf", xmlFile)
		if fail {
			require.ErrorContains(t, err, "simulated USB failure")
		} else {
			require.NoError(t, err)
		}
		require.GreaterOrEqual(t, len(events), 2)
		last := events[len(events)-1]
		require.True(t, last.Finished)
		require.Equal(t, fail, last.Failed)
		require.Equal(t, uint64(4), last.Done)
	}
}

func TestTransferProgressReadAndFailure(t *testing.T) {
	dir := t.TempDir()
	file, err := writeXML(dir, "capture.xml", []operation{{XMLName: xml.Name{Local: "read"}, File: "part.bin", Count: 8, Sector: 512}})
	require.NoError(t, err)
	var events []Progress
	ctx := WithProgress(context.Background(), func(p Progress) { events = append(events, p) })
	tracker, err := newTransferProgress(ctx, dir, "12345678", []string{file})
	require.NoError(t, err)
	tracker.tick()
	require.Zero(t, events[0].Done)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "part.bin"), make([]byte, 1024), 0600))
	tracker.tick()
	require.Equal(t, uint64(1024), events[1].Done)
	require.Equal(t, uint64(4096), events[1].Total)
	tracker.finish(errors.New("USB disconnected"))
	last := events[len(events)-1]
	require.True(t, last.Failed)
	require.True(t, last.Finished)
	require.Equal(t, uint64(1024), last.Done)
}

func TestTransferProgressAcknowledgementsAndPhaseChange(t *testing.T) {
	dir := t.TempDir()
	// Repeated filenames occur when --skip-unchanged writes separate regions.
	ops := []operation{{XMLName: xml.Name{Local: "program"}, File: "part.bin", Count: 2, Sector: 512}, {XMLName: xml.Name{Local: "program"}, File: "part.bin", Count: 4, Sector: 512, Start: 4}}
	write, err := writeXML(dir, "restore.xml", ops)
	require.NoError(t, err)
	read, err := writeXML(dir, "verify.xml", []operation{{XMLName: xml.Name{Local: "read"}, File: "part.bin", Count: 8, Sector: 512}})
	require.NoError(t, err)
	var events []Progress
	ctx := WithProgress(context.Background(), func(p Progress) { events = append(events, p) })
	tracker, err := newTransferProgress(ctx, dir, "12345678", []string{write, read})
	require.NoError(t, err)
	tracker.tick()
	// An unrelated device log must not advance progress.
	_, err = tracker.Write([]byte("LOG: flashed \"part.bin\" successfully\n"))
	require.NoError(t, err)
	tracker.tick()
	require.Zero(t, events[len(events)-1].Done)
	for _, fragment := range []string{"flas", "hed \"part.bin\" successfully at 40kB/s\r", "\n"} {
		_, err = tracker.Write([]byte(fragment))
		require.NoError(t, err)
	}
	tracker.tick()
	require.Equal(t, uint64(1024), events[len(events)-1].Done)
	_, err = tracker.Write([]byte("flashed \"part.bin\" successfully\n"))
	require.NoError(t, err)
	require.Equal(t, "readback", events[len(events)-1].Phase)
	require.Zero(t, events[len(events)-1].Done)
	prev := events[len(events)-2]
	require.True(t, prev.Finished)
	require.Equal(t, prev.Total, prev.Done)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "part.bin"), make([]byte, 4096), 0600))
	_, err = tracker.Write([]byte("read \"part.bin\" successfully\n"))
	require.NoError(t, err)
	tracker.finish(nil)
	last := events[len(events)-1]
	require.False(t, last.Failed)
	require.Equal(t, last.Total, last.Done)
	// Long unrecognized diagnostics remain bounded.
	_, err = tracker.Write([]byte(strings.Repeat("x", 20000)))
	require.NoError(t, err)
	require.LessOrEqual(t, len(tracker.pending), 8192)
}

func TestConcurrentChecksumProgressAndStaleReadbackRemoval(t *testing.T) {
	dir, f := fixture(t, "unoq", 512)
	f.disks["00000002"] = map[int][]byte{0: diskImage(512, 32), 1: make([]byte, 2048)}
	f.disks["00000003"] = map[int][]byte{0: diskImage(512, 32), 1: make([]byte, 2048)}
	f.disks["00000002"][0][4096] ^= 1
	f.disks["00000003"][0][4096] ^= 1
	var mu sync.Mutex
	var events []Progress
	ctx := WithProgress(context.Background(), func(p Progress) { mu.Lock(); defer mu.Unlock(); events = append(events, p) })
	run := func(ctx context.Context, work, s, storage, loader string, files ...string) error {
		for _, file := range files {
			if filepath.Base(file) == "verify.xml" {
				_, err := os.Stat(filepath.Join(work, "lun0-000000000000.bin"))
				if !os.IsNotExist(err) {
					return errors.New("stale pre-read file at verification start")
				}
			}
		}
		return f.run(ctx, work, s, storage, loader, files...)
	}
	opts := options("unoq", "2", "3")
	opts.SkipUnchanged = true
	results, err := Flash(ctx, run, dir, opts)
	require.NoError(t, err)
	require.Len(t, results, 2)
	finished := map[string]bool{}
	for _, p := range events {
		require.LessOrEqual(t, p.Done, p.Total)
		if p.Finished {
			require.False(t, p.Failed)
			require.Equal(t, p.Total, p.Done)
			finished[p.Serial+":"+p.Phase] = true
		}
	}
	require.True(t, finished[":image checksums"])
	for _, serial := range []string{"00000002", "00000003"} {
		require.True(t, finished[serial+":compare contents"])
		require.True(t, finished[serial+":verify checksums"])
	}
}
