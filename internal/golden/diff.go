// This file is part of arduino-flasher-cli.
//
// SPDX-FileCopyrightText: Arduino s.r.l. and/or its affiliated companies
// SPDX-License-Identifier: GPL-3.0-or-later

package golden

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const compareBlockBytes = uint64(1 << 20)

// differingRegions compares actual target bytes, including unused space. Adjacent
// differing regions are coalesced; matching zeroes are skipped only after reading
// them from the target, never inferred from the image alone.
func differingRegions(ctx context.Context, source, target string, part Part, sector uint64) ([]operation, error) {
	src, err := os.Open(source)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	dst, err := os.Open(target)
	if err != nil {
		return nil, err
	}
	defer dst.Close()
	total := part.Sectors * sector
	for _, f := range []*os.File{src, dst} {
		st, err := f.Stat()
		if err != nil {
			return nil, err
		}
		if !st.Mode().IsRegular() || uint64(st.Size()) != total {
			return nil, fmt.Errorf("comparison size mismatch: %s", f.Name())
		}
	}
	a, b := make([]byte, compareBlockBytes), make([]byte, compareBlockBytes)
	var ops []operation
	for offset := uint64(0); offset < total; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n := min(compareBlockBytes, total-offset)
		if _, err := io.ReadFull(src, a[:n]); err != nil {
			return nil, err
		}
		if _, err := io.ReadFull(dst, b[:n]); err != nil {
			return nil, err
		}
		if !bytes.Equal(a[:n], b[:n]) {
			start := part.Start + offset/sector
			count := n / sector
			if len(ops) > 0 && ops[len(ops)-1].Start+ops[len(ops)-1].Count == start {
				ops[len(ops)-1].Count += count
			} else {
				ops = append(ops, operation{XMLName: xml.Name{Local: "program"}, Sector: sector, File: source, LUN: part.LUN, Count: count, Start: start, FileOffset: offset / sector, Sparse: "false"})
			}
		}
		advanceProgress(ctx, n)
		offset += n
	}
	return ops, nil
}

func compareImage(ctx context.Context, source, target, serial string, m Manifest) (ops []operation, retErr error) {
	ctx, progress := beginProgress(ctx, serial, "compare contents", manifestBytes(m))
	defer func() { progress.finish(retErr) }()
	for _, p := range m.Parts {
		changed, err := differingRegions(ctx, filepath.Join(source, p.File), filepath.Join(target, p.File), p, sectorFor(m, p.LUN))
		if err != nil {
			return nil, err
		}
		ops = append(ops, changed...)
	}
	return ops, nil
}
