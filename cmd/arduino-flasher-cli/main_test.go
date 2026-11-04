// This file is part of arduino-flasher-cli.
//
// SPDX-FileCopyrightText: Arduino s.r.l. and/or its affiliated companies
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/arduino/arduino-flasher-cli/cmd/arduino-flasher-cli/golden"
)

func goldenTestRoot(args []string) (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	root := &cobra.Command{Use: "arduino-flasher-cli", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(golden.NewCommand())
	var out, diagnostics bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&diagnostics)
	root.SetArgs(args)
	return root, &out, &diagnostics
}

func TestGoldenErrorsShowRelevantUsage(t *testing.T) {
	for _, tc := range []struct {
		name          string
		args          []string
		usage, option string
	}{
		{"unknown command", []string{"golden", "captur"}, "arduino-flasher-cli golden", "capture"},
		{"unknown group flag", []string{"golden", "--invalid"}, "arduino-flasher-cli golden", "flash"},
		{"missing capture output", []string{"golden", "capture"}, "golden capture OUTPUT", "--programmer"},
		{"extra capture argument", []string{"golden", "capture", "one", "two"}, "golden capture OUTPUT", "--serial"},
		{"missing board", []string{"golden", "capture", "image"}, "golden capture OUTPUT", "--board"},
		{"unknown capture flag", []string{"golden", "capture", "--invalid"}, "golden capture OUTPUT", "--programmer"},
		{"invalid flag value", []string{"golden", "capture", "image", "--timeout", "wrong"}, "golden capture OUTPUT", "--timeout"},
		{"invalid board", []string{"golden", "capture", "image", "--board", "wrong"}, "golden capture OUTPUT", "--board"},
		{"existing output", []string{"golden", "capture", t.TempDir(), "--board", "unoq"}, "golden capture OUTPUT", "--board"},
		{"missing flash image", []string{"golden", "flash"}, "golden flash IMAGE", "--jobs"},
		{"unknown flash flag", []string{"golden", "flash", "--invalid"}, "golden flash IMAGE", "--serial"},
		{"missing target selection", []string{"golden", "flash", "image", "--board", "unoq", "--dry-run"}, "golden flash IMAGE", "--all"},
		{"conflicting flags", []string{"golden", "flash", "image", "--board", "unoq", "--serial", "2", "--all", "--dry-run"}, "golden flash IMAGE", "--serial"},
		{"missing authorization", []string{"golden", "flash", "image", "--board", "unoq", "--serial", "2"}, "golden flash IMAGE", "--yes"},
		{"extra devices argument", []string{"golden", "devices", "unexpected"}, "golden devices", "--help"},
		{"unknown devices flag", []string{"golden", "devices", "--invalid"}, "golden devices", "--help"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, out, diagnostics := goldenTestRoot(tc.args)
			err := executeCommand(context.Background(), root)
			require.Error(t, err)
			require.Empty(t, out.String(), "error help must not contaminate JSON stdout")
			require.Contains(t, diagnostics.String(), "Error: "+err.Error())
			require.Contains(t, diagnostics.String(), "Usage:")
			require.Contains(t, diagnostics.String(), tc.usage)
			require.Contains(t, diagnostics.String(), tc.option)
			require.Equal(t, 1, bytes.Count(diagnostics.Bytes(), []byte("Usage:")))
		})
	}
}

func TestGoldenExplicitHelpSucceeds(t *testing.T) {
	for _, args := range [][]string{
		{"golden"}, {"golden", "--help"}, {"golden", "capture", "--help"},
		{"golden", "flash", "--help"}, {"golden", "devices", "--help"},
	} {
		root, out, diagnostics := goldenTestRoot(args)
		require.NoError(t, executeCommand(context.Background(), root))
		require.Contains(t, out.String(), "Usage:")
		require.Empty(t, diagnostics.String())
	}
}
