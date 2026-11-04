// This file is part of arduino-flasher-cli.
//
// SPDX-FileCopyrightText: Arduino s.r.l. and/or its affiliated companies
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"
	"go.bug.st/cleanup"

	"github.com/arduino/arduino-flasher-cli/cmd/arduino-flasher-cli/cache"
	"github.com/arduino/arduino-flasher-cli/cmd/arduino-flasher-cli/daemon"
	"github.com/arduino/arduino-flasher-cli/cmd/arduino-flasher-cli/download"
	"github.com/arduino/arduino-flasher-cli/cmd/arduino-flasher-cli/drivers"
	"github.com/arduino/arduino-flasher-cli/cmd/arduino-flasher-cli/flash"
	"github.com/arduino/arduino-flasher-cli/cmd/arduino-flasher-cli/golden"
	"github.com/arduino/arduino-flasher-cli/cmd/arduino-flasher-cli/list"
	"github.com/arduino/arduino-flasher-cli/cmd/arduino-flasher-cli/version"
	"github.com/arduino/arduino-flasher-cli/cmd/feedback"
	"github.com/arduino/arduino-flasher-cli/cmd/i18n"
	"github.com/arduino/arduino-flasher-cli/service"
)

// Version will be set a build time with -ldflags
var Version string = "0.0.0-dev"
var format string

func main() {
	rootCmd := &cobra.Command{
		Use:   "arduino-flasher-cli",
		Short: "A CLI to update your Arduino UNO Q board, by downloading and flashing the latest Arduino Linux image",
		Example: " " + os.Args[0] + " flash unoq\n" +
			" " + os.Args[0] + " list\n",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			parsedFormat, ok := feedback.ParseOutputFormat(format)
			if !ok {
				return fmt.Errorf("%s", i18n.Tr("Invalid output format: %s", format))
			}
			feedback.SetFormat(parsedFormat)
			return nil
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	rootCmd.PersistentFlags().StringVar(&format, "format", "text", "Output format (text, json)")

	rootCmd.AddCommand(
		flash.NewFlashCmd(),
		golden.NewCommand(),
		drivers.NewInstallDriversCmd(),
		list.NewListCmd(),
		download.NewDownloadCmd(),
		cache.NewCacheCmd(),
		version.NewVersionCmd(Version),
		daemon.NewDaemonCommand(service.NewFlasherServer()),
	)

	ctx := context.Background()
	ctx, _ = cleanup.InterruptableContext(ctx)
	if err := executeCommand(ctx, rootCmd); err != nil {
		os.Exit(1)
	}
}

// The application silences Cobra's automatic usage globally. Golden workflows
// show the failing subcommand's usage explicitly, including errors detected
// before RunE (unknown commands, flag parsing, and required flag groups).
func executeCommand(ctx context.Context, root *cobra.Command) error {
	cmd, err := root.ExecuteContextC(ctx)
	if err == nil {
		return nil
	}
	for parent := cmd; parent != nil; parent = parent.Parent() {
		if parent.Name() == "golden" {
			fmt.Fprintf(cmd.ErrOrStderr(), "Error: %v\n\n%s", err, cmd.UsageString())
			return err
		}
	}
	slog.Error(err.Error())
	return err
}
