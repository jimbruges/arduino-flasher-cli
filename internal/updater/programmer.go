// This file is part of arduino-flasher-cli.
//
// SPDX-FileCopyrightText: Arduino s.r.l. and/or its affiliated companies
// SPDX-License-Identifier: GPL-3.0-or-later

package updater

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/arduino/arduino-flasher-cli/internal/registry"
	"github.com/klauspost/compress/zstd"
)

// ProgrammerFromRelease reuses or downloads a checksum-verified official archive
// and extracts only its top-level flash programmer. The caller must run cleanup
// after Capture has copied the programmer into its bundle.
func ProgrammerFromRelease(ctx context.Context, rel registry.Release) (string, func(), error) {
	archive, err := DownloadImageToCache(ctx, rel)
	if err != nil {
		return "", nil, err
	}
	f, err := os.Open(archive.String())
	if err != nil {
		return "", nil, err
	}
	defer f.Close()
	data, err := extractProgrammer(ctx, f)
	if err != nil {
		return "", nil, fmt.Errorf("extract programmer from %s: %w", rel.FileName, err)
	}
	dir, err := os.MkdirTemp("", "golden-programmer-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	file := filepath.Join(dir, "prog_firehose_ddr.elf")
	if err := os.WriteFile(file, data, 0600); err != nil {
		cleanup()
		return "", nil, err
	}
	return file, cleanup, nil
}

type programmerReader struct {
	ctx context.Context
	r   io.Reader
}

func (r programmerReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func extractProgrammer(ctx context.Context, archive io.Reader) ([]byte, error) {
	decoder, err := zstd.NewReader(programmerReader{ctx, archive}, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(256<<20))
	if err != nil {
		return nil, err
	}
	defer decoder.Close()
	tr := tar.NewReader(programmerReader{ctx, decoder})
	var programmer []byte
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		name := strings.TrimPrefix(h.Name, "./")
		parts := strings.Split(name, "/")
		// Accept flash/file or one archive root followed by flash/file. Exclude
		// component programmers and traversal; never extract archive paths.
		if name != path.Clean(name) || path.IsAbs(name) || (len(parts) != 2 && len(parts) != 3) || parts[0] == ".." {
			continue
		}
		parent := parts[len(parts)-2]
		if (parent != "flash" && parent != "flash_UnoQ") || parts[len(parts)-1] != "prog_firehose_ddr.elf" {
			continue
		}
		if programmer != nil {
			return nil, fmt.Errorf("multiple flash programmers found; specify --programmer explicitly")
		}
		if h.Typeflag != tar.TypeReg || h.Size < 4 || h.Size > 32<<20 {
			return nil, fmt.Errorf("invalid programmer entry %q", h.Name)
		}
		programmer, err = io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		if string(programmer[:4]) != "\x7fELF" {
			return nil, fmt.Errorf("programmer is not an ELF file")
		}
	}
	if programmer == nil {
		return nil, fmt.Errorf("no flash/prog_firehose_ddr.elf found; specify --programmer explicitly")
	}
	return programmer, ctx.Err()
}
