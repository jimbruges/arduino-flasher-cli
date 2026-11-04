// This file is part of arduino-flasher-cli.
//
// SPDX-FileCopyrightText: Arduino s.r.l. and/or its affiliated companies
// SPDX-License-Identifier: GPL-3.0-or-later

package golden

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/arduino/arduino-flasher-cli/internal/edl"
	"github.com/arduino/arduino-flasher-cli/internal/golden"
)

func TestCaptureSourceSelection(t *testing.T) {
	for _, tc := range []struct {
		name, explicit string
		devices        []edl.Device
		discoveryErr   error
		want, wantErr  string
	}{
		{name: "sole device", devices: []edl.Device{{Serial: "0x12345678"}}, want: "0x12345678"},
		{name: "none", wantErr: "no connected Qualcomm EDL device"},
		{name: "multiple", devices: []edl.Device{{Serial: "0x12345678"}, {Serial: "0x87654321"}}, wantErr: "multiple EDL devices connected (0x12345678, 0x87654321)"},
		{name: "discovery failure", discoveryErr: errors.New("inventory failed"), wantErr: "inventory failed"},
		{name: "explicit bypasses discovery", explicit: "2", want: "0x00000002"},
		{name: "invalid explicit", explicit: "invalid", wantErr: "serial"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := captureSource(context.Background(), tc.explicit, func(context.Context) ([]edl.Device, error) {
				if tc.explicit != "" {
					t.Fatal("explicit serial triggered discovery")
				}
				return tc.devices, tc.discoveryErr
			})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("got %q, %v", got, err)
				}
			} else if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestCommandsRejectMissingAuthorizationAndInputs(t *testing.T) {
	for _, args := range [][]string{
		{"flash", "image", "--board", "unoq", "--serial", "2"},
		{"flash", "image", "--board", "unoq", "--serial", "2", "--restore-source"},
		{"capture", "image"},
		{"flash", "image", "--yes", "--board", "unoq"},
	} {
		cmd := NewCommand()
		cmd.SetArgs(args)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		if err := cmd.Execute(); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
func TestDryRunDoesNotRequireConfirmation(t *testing.T) {
	cmd := NewCommand()
	cmd.SetArgs([]string{"flash", t.TempDir(), "--board", "unoq", "--serial", "2", "--dry-run"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	if err == nil || strings.Contains(err.Error(), "--yes") {
		t.Fatalf("expected missing manifest, got %v", err)
	}
}

func TestCaptureAutomaticProgrammerFlags(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		wantErr string
	}{
		{[]string{"capture", t.TempDir(), "--board", "unoq"}, "capture output already exists"},
		{[]string{"capture", "unused", "--board", "unknown"}, "unknown board"},
		{[]string{"capture", "unused", "--board", "unoq", "--programmer", "local.elf", "--version", "123"}, "group"},
		{[]string{"capture", "unused", "--board", "unoq", "--programmer", "local.elf", "--os", "debian"}, "group"},
	} {
		cmd := NewCommand()
		cmd.SetArgs(tc.args)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Fatalf("%v: expected %q, got %v", tc.args, tc.wantErr, err)
		}
	}
}

func TestAutoFlagsAreExclusive(t *testing.T) {
	for _, extra := range [][]string{{"--serial", "2"}, {"--restore-source"}} {
		cmd := NewCommand()
		args := []string{"flash", "missing", "--board", "unoq", "--all", "--dry-run"}
		cmd.SetArgs(append(args, extra...))
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "group") {
			t.Fatalf("expected flag conflict: %v", err)
		}
	}
	cmd := NewCommand()
	cmd.SetArgs([]string{"flash", "missing", "--board", "unoq", "--all", "--dry-run"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "manifest.json") {
		t.Fatalf("--all did not replace --serial requirement: %v", err)
	}
}

func TestCompletionMessagesDistinguishFailures(t *testing.T) {
	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetErr(&out)
	report := completionLogger(cmd, &sync.Mutex{})
	report(golden.Result{Serial: "11111111", Status: "verified", DurationSeconds: 3})
	if !strings.Contains(out.String(), "[0x11111111] VERIFIED") || !strings.Contains(out.String(), "safe to remove EDL jumper") {
		t.Fatal(out.String())
	}
	out.Reset()
	report(golden.Result{Serial: "22222222", Status: "failed", Error: "readback mismatch"})
	if !strings.Contains(out.String(), "FAILED: readback mismatch") || strings.Contains(out.String(), "VERIFIED") {
		t.Fatal(out.String())
	}
}
