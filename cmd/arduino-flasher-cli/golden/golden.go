// This file is part of arduino-flasher-cli.
//
// SPDX-FileCopyrightText: Arduino s.r.l. and/or its affiliated companies
// SPDX-License-Identifier: GPL-3.0-or-later

package golden

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/arduino/arduino-flasher-cli/internal/edl"
	"github.com/arduino/arduino-flasher-cli/internal/golden"
	"github.com/arduino/arduino-flasher-cli/internal/registry"
	"github.com/arduino/arduino-flasher-cli/internal/updater"
)

func NewCommand() *cobra.Command {
	root := &cobra.Command{
		Use: "golden", Short: "Capture offline storage images and clone explicit targets concurrently",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	var board, storage, source, programmer, luns string
	var imageOS, imageVersion string
	var captureVerbose, flashVerbose bool
	var rawLUNs []string
	var sector uint64
	var timeout time.Duration
	capture := &cobra.Command{Use: "capture OUTPUT", Short: "Capture selected storage LUNs from an EDL source", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if timeout <= 0 {
			return fmt.Errorf("timeout must be positive")
		}
		boardInfo, ok := registry.BoardByID(board)
		if !ok {
			return fmt.Errorf("unknown board %q: use unoq or ventunoq", board)
		}
		if _, err := os.Lstat(args[0]); err == nil {
			return fmt.Errorf("capture output already exists: %s", args[0])
		} else if !os.IsNotExist(err) {
			return err
		}
		selectedLUNs := luns
		if selectedLUNs == "auto" {
			selectedLUNs = "0"
			if board == "ventunoq" {
				selectedLUNs = "0,1"
			}
		}
		units, err := golden.ParseLUNs(selectedLUNs, sector)
		if err != nil {
			return err
		}
		for _, raw := range rawLUNs {
			l, err := golden.ParseRawLUN(raw, sector)
			if err != nil {
				return err
			}
			units = append(units, l)
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
		defer cancel()
		started := time.Now()
		logMu := &sync.Mutex{}
		ctx = golden.WithProgress(ctx, newProgressLog(cmd, logMu, captureVerbose).update)
		selectedSource, err := captureSource(ctx, source, edl.Discover)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "Capture source: %s\n", selectedSource)
		setupStarted := time.Now()
		selectedProgrammer := programmer
		if selectedProgrammer == "" {
			fmt.Fprintln(cmd.ErrOrStderr(), "Resolving official image for the Firehose programmer...")
			releases, fetchErr := registry.NewClient().Fetch(ctx)
			if fetchErr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Image index warning: %v\n", fetchErr)
			}
			selectedOS := imageOS
			if selectedOS == "" {
				selectedOS = boardInfo.DefaultOs
			}
			rel, err := releases.Resolve(selectedOS, board, imageVersion)
			if err != nil {
				return fmt.Errorf("resolve programmer image: %w", err)
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Using %s %s image %s; checking cache or downloading archive, then extracting programmer\n", board, rel.OS, rel.Version)
			var cleanup func()
			selectedProgrammer, cleanup, err = updater.ProgrammerFromRelease(ctx, rel)
			if err != nil {
				return err
			}
			defer cleanup()
			fmt.Fprintf(cmd.ErrOrStderr(), "Programmer ready (elapsed %s)\n", time.Since(setupStarted).Round(time.Second))
		}
		qdl, clean, err := updater.InstallQDL()
		if err != nil {
			return err
		}
		defer clean()
		if err = golden.Capture(ctx, golden.CommandRunner(qdl.String()), board, storage, selectedSource, selectedProgrammer, args[0], units); err != nil {
			return err
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "[%s] CAPTURED — elapsed %s (including setup)\n", selectedSource, time.Since(started).Round(time.Second))
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"status": "captured", "path": args[0], "duration_seconds": time.Since(started).Seconds()})
	}}
	capture.Flags().StringVar(&board, "board", "", "Source board: unoq or ventunoq")
	capture.Flags().StringVar(&storage, "storage", "emmc", "Storage type from the board's official image: emmc or ufs")
	capture.Flags().StringVar(&source, "serial", "", "Source EDL serial (decimal or 0x-prefixed hex); omitted selects the sole connected EDL device")
	capture.Flags().StringVar(&programmer, "programmer", "", "Local Firehose ELF programmer; omitted downloads or reuses the official image")
	capture.Flags().StringVar(&imageVersion, "version", "", "Official image version for automatic programmer setup (default: latest)")
	capture.Flags().StringVar(&imageOS, "os", "", "Official image distribution for automatic programmer setup (default: board default)")
	capture.Flags().StringVar(&luns, "luns", "auto", "GPT-backed physical LUNs: auto selects 0 for UNO Q, 0,1 for VENTUNO Q; or a comma-separated list")
	capture.Flags().StringSliceVar(&rawLUNs, "raw-lun", nil, "Non-GPT boot LUN as NUMBER:SECTORS from official geometry (repeatable)")
	capture.Flags().Uint64Var(&sector, "sector-size", 512, "Logical sector size from the official image (512 or 4096)")
	capture.Flags().BoolVar(&captureVerbose, "verbose-progress", false, "Show all phases and frequent progress updates")
	capture.Flags().DurationVar(&timeout, "timeout", 2*time.Hour, "Whole capture timeout")
	_ = capture.MarkFlagRequired("board")
	capture.MarkFlagsMutuallyExclusive("programmer", "version")
	capture.MarkFlagsMutuallyExclusive("programmer", "os")
	var opts golden.FlashOptions
	var yes bool
	var digestQDL string
	var digestBlockMiB uint64
	flash := &cobra.Command{Use: "flash IMAGE", Short: "Restore and verify a golden image on explicit EDL targets", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !yes && !opts.DryRun {
			return fmt.Errorf("restoring replaces all selected LUN data; specify --yes to authorize the explicit targets, or --dry-run to inspect")
		}
		opts.DigestBlockBytes = digestBlockMiB << 20
		if digestBlockMiB < 1 || digestBlockMiB > 1024 {
			return fmt.Errorf("digest block size must be 1..1024 MiB")
		}
		if opts.Verification == "digest" && !opts.DryRun {
			if digestQDL == "" {
				return fmt.Errorf("digest mode needs --digest-qdl; build it with scripts/qdl-digest/build.sh, or use --verification readback")
			}
			resolved, err := exec.LookPath(digestQDL)
			if err != nil {
				return fmt.Errorf("digest QDL: %w", err)
			}
			resolved, err = filepath.Abs(resolved)
			if err != nil {
				return err
			}
			opts.DigestRun = golden.CommandRunner(resolved)
		}
		started := time.Now()
		var run golden.Run
		logMu := &sync.Mutex{}
		opts.OnResult = completionLogger(cmd, logMu)
		progress := newProgressLog(cmd, logMu, flashVerbose)
		if !opts.DryRun {
			qdl, clean, err := updater.InstallQDL()
			if err != nil {
				return err
			}
			defer clean()
			run = golden.CommandRunner(qdl.String())
		}
		ctx := golden.WithProgress(cmd.Context(), progress.update)
		results, err := golden.Flash(ctx, run, args[0], opts)
		verified, failed := 0, 0
		for _, r := range results {
			if r.Status == "verified" {
				verified++
			}
			if r.Status == "failed" {
				failed++
			}
		}
		if opts.DryRun {
			fmt.Fprintf(cmd.ErrOrStderr(), "Plan checked in %s — %d targets\n", time.Since(started).Round(time.Second), len(results))
		} else {
			fmt.Fprintf(cmd.ErrOrStderr(), "Batch elapsed %s — %d verified, %d failed\n", time.Since(started).Round(time.Second), verified, failed)
			progress.summary(results)
		}
		if results != nil {
			if outputErr := json.NewEncoder(cmd.OutOrStdout()).Encode(results); outputErr != nil {
				return outputErr
			}
		}
		return err
	}}
	flash.Flags().StringVar(&opts.Verification, "verification", "readback", "Full-image verification: readback or digest")
	flash.Flags().StringVar(&digestQDL, "digest-qdl", "", "Path to QDL with storage digest support (scripts/qdl-digest/build.sh)")
	flash.Flags().Uint64Var(&digestBlockMiB, "digest-block-mib", 64, "Digest comparison granularity in MiB: power of two, 1..1024")
	flash.Flags().StringVar(&opts.Board, "board", "", "Target board: must match the bundle")
	flash.Flags().StringSliceVar(&opts.Serials, "serial", nil, "Explicit target serials; comma-separated or repeated (decimal or 0x-prefixed hex)")
	flash.Flags().BoolVar(&flashVerbose, "verbose-progress", false, "Show all phases and frequent progress updates")
	flash.Flags().IntVar(&opts.Jobs, "jobs", 16, "Maximum concurrent boards (1..32)")
	flash.Flags().StringVar(&opts.TempDir, "temp-dir", "", "Scratch directory parent; readback mode needs one image worth of space per worker")
	flash.Flags().DurationVar(&opts.Timeout, "timeout", 2*time.Hour, "Timeout per target, including preflight, writes and verification")
	flash.Flags().BoolVar(&opts.SkipUnchanged, "skip-unchanged", false, "Compare targets first; write differing regions, then verify the full image")
	flash.Flags().BoolVar(&opts.All, "all", false, "Snapshot all connected serial-addressable Qualcomm EDL targets, excluding source; board model is not auto-detected")
	flash.Flags().BoolVar(&opts.DryRun, "dry-run", false, "Verify bundle and target selection without accessing USB")
	flash.Flags().BoolVar(&opts.RestoreSource, "restore-source", false, "Allow restoring only the original source board (single target; requires --yes)")
	flash.Flags().BoolVarP(&yes, "yes", "y", false, "Authorize replacing data on all explicit targets")
	_ = flash.MarkFlagRequired("board")
	flash.MarkFlagsOneRequired("serial", "all")
	flash.MarkFlagsMutuallyExclusive("serial", "all")
	flash.MarkFlagsMutuallyExclusive("restore-source", "all")
	devices := &cobra.Command{Use: "devices", Short: "List connected Qualcomm EDL candidates (does not detect board model)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		found, err := edl.Discover(cmd.Context())
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(found)
	}}
	root.AddCommand(capture, flash, devices)
	return root
}

func captureSource(ctx context.Context, explicit string, discover func(context.Context) ([]edl.Device, error)) (string, error) {
	if explicit != "" {
		s, err := golden.NormalizeSerial(explicit)
		if err != nil {
			return "", err
		}
		return "0x" + s, nil
	}
	devices, err := discover(ctx)
	if err != nil {
		return "", err
	}
	if len(devices) == 0 {
		return "", fmt.Errorf("no connected Qualcomm EDL device found; connect the source in EDL mode or specify --serial")
	}
	if len(devices) != 1 {
		serials := make([]string, len(devices))
		for i, d := range devices {
			serials[i] = d.Serial
		}
		return "", fmt.Errorf("multiple EDL devices connected (%s); select the source with --serial", strings.Join(serials, ", "))
	}
	return devices[0].Serial, nil
}

func completionLogger(cmd *cobra.Command, mu *sync.Mutex) func(golden.Result) {
	return func(r golden.Result) {
		mu.Lock()
		defer mu.Unlock()
		if r.Status == "verified" {
			fmt.Fprintf(cmd.ErrOrStderr(), "[0x%s] VERIFIED — safe to remove EDL jumper and power-cycle (%s)\n", r.Serial, (time.Duration(r.DurationSeconds * float64(time.Second))).Round(time.Second))
		} else {
			fmt.Fprintf(cmd.ErrOrStderr(), "[0x%s] FAILED: %s\n", r.Serial, r.Error)
		}
	}
}
