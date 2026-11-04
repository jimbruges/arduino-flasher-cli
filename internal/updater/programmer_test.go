// This file is part of arduino-flasher-cli.
//
// SPDX-FileCopyrightText: Arduino s.r.l. and/or its affiliated companies
// SPDX-License-Identifier: GPL-3.0-or-later

package updater

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
)

func programmerArchive(t *testing.T, names []string, content string, kind byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	z, err := zstd.NewWriter(&buf)
	require.NoError(t, err)
	w := tar.NewWriter(z)
	for _, name := range names {
		h := &tar.Header{Name: name, Mode: 0600, Typeflag: kind, Size: int64(len(content))}
		if kind == tar.TypeSymlink {
			h.Size = 0
			h.Linkname = "elsewhere"
		}
		require.NoError(t, w.WriteHeader(h))
		if h.Size > 0 {
			_, err = w.Write([]byte(content))
			require.NoError(t, err)
		}
	}
	require.NoError(t, w.Close())
	require.NoError(t, z.Close())
	return buf.Bytes()
}

func TestExtractProgrammer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		paths   []string
		content string
		kind    byte
		wantErr string
	}{
		{"uno", []string{"image/flash/prog_firehose_ddr.elf", "image/components/flash/prog_firehose_ddr.elf"}, "\x7fELFtest", tar.TypeReg, ""},
		{"legacy", []string{"./image/flash_UnoQ/prog_firehose_ddr.elf"}, "\x7fELFtest", tar.TypeReg, ""},
		{"flat", []string{"flash/prog_firehose_ddr.elf"}, "\x7fELFtest", tar.TypeReg, ""},
		{"missing", []string{"image/components/flash/prog_firehose_ddr.elf", "../flash/prog_firehose_ddr.elf"}, "\x7fELFtest", tar.TypeReg, "no flash/"},
		{"ambiguous", []string{"a/flash/prog_firehose_ddr.elf", "b/flash/prog_firehose_ddr.elf"}, "\x7fELFtest", tar.TypeReg, "multiple"},
		{"link", []string{"image/flash/prog_firehose_ddr.elf"}, "", tar.TypeSymlink, "invalid programmer"},
		{"not ELF", []string{"image/flash/prog_firehose_ddr.elf"}, "not an ELF", tar.TypeReg, "not an ELF"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := programmerArchive(t, tc.paths, tc.content, tc.kind)
			got, err := extractProgrammer(context.Background(), bytes.NewReader(data))
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.content, string(got))
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := extractProgrammer(ctx, bytes.NewReader(nil))
	require.ErrorIs(t, err, context.Canceled)
}

func TestProgrammerDownloadAndCacheReuse(t *testing.T) {
	_, rel, transport := setupImageDownload(t)
	transport.contents = programmerArchive(t, []string{"image/flash/prog_firehose_ddr.elf"}, "\x7fELFtest", tar.TypeReg)
	rel.Sha256 = fmt.Sprintf("%x", sha256.Sum256(transport.contents))
	for i := 0; i < 2; i++ {
		before := transport.requests
		file, cleanup, err := ProgrammerFromRelease(context.Background(), rel)
		require.NoError(t, err)
		t.Cleanup(cleanup)
		data, err := os.ReadFile(file)
		require.NoError(t, err)
		require.Equal(t, "\x7fELFtest", string(data))
		if i == 0 {
			require.Greater(t, transport.requests, before)
		} else {
			require.Equal(t, before, transport.requests)
		}
		cleanup()
		_, err = os.Stat(file)
		require.True(t, os.IsNotExist(err))
	}
}

func TestOfficialProgrammerExtraction(t *testing.T) {
	archive, expected := os.Getenv("GOLDEN_TEST_OFFICIAL_ARCHIVE"), os.Getenv("GOLDEN_TEST_OFFICIAL_PROGRAMMER")
	if archive == "" || expected == "" {
		t.Skip("set official archive and extracted programmer paths for hardware-image compatibility check")
	}
	f, err := os.Open(archive)
	require.NoError(t, err)
	defer f.Close()
	got, err := extractProgrammer(context.Background(), f)
	require.NoError(t, err)
	want, err := os.ReadFile(expected)
	require.NoError(t, err)
	require.Equal(t, sha256.Sum256(want), sha256.Sum256(got))
}
