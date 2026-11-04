// SPDX-FileCopyrightText: Arduino s.r.l. and/or its affiliated companies
// SPDX-License-Identifier: GPL-3.0-or-later

package golden

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(b)
}

type digestRange struct {
	op       operation
	expected string
}
type imageHashes struct{ fine [][]digestRange }

// Prepare once per batch, never once per target. Manifest hashes already cover
// the coarse ranges; finer hashes are needed only for partial writes.
func prepareImageHashes(ctx context.Context, dir string, m Manifest, block uint64) (out *imageHashes, retErr error) {
	ctx, progress := beginProgress(ctx, "", "prepare block digests", manifestBytes(m))
	defer func() { progress.finish(retErr) }()
	out = &imageHashes{fine: make([][]digestRange, len(m.Parts))}
	for i, p := range m.Parts {
		f, err := os.Open(filepath.Join(dir, p.File))
		if err != nil {
			return nil, err
		}
		sector := sectorFor(m, p.LUN)
		for offset := uint64(0); offset < p.Sectors*sector; {
			n := min(block, p.Sectors*sector-offset)
			h := sha256.New()
			var copied int64
			copied, err = io.Copy(h, &contextReader{ctx: ctx, r: io.NewSectionReader(f, int64(offset), int64(n))})
			if err == nil && uint64(copied) != n {
				err = io.ErrUnexpectedEOF
			}
			if err != nil {
				f.Close()
				return nil, err
			}
			out.fine[i] = append(out.fine[i], digestRange{op: operation{Sector: sector, LUN: p.LUN, Start: p.Start + offset/sector, Count: n / sector, File: filepath.Join(dir, p.File), FileOffset: offset / sector, Sparse: "false"}, expected: hex.EncodeToString(h.Sum(nil))})
			advanceProgress(ctx, n)
			offset += n
		}
		if err = f.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func coarseDigests(dir string, m Manifest) []digestRange {
	ranges := make([]digestRange, 0, len(m.Parts))
	for _, p := range m.Parts {
		ranges = append(ranges, digestRange{op: operation{Sector: sectorFor(m, p.LUN), LUN: p.LUN, Start: p.Start, Count: p.Sectors, File: filepath.Join(dir, p.File), Sparse: "false"}, expected: p.SHA256})
	}
	return ranges
}

// Each call uses fresh unique output names. A successful subprocess exit alone
// never verifies storage: all outputs must contain exactly one SHA-256 digest.
func queryStorageDigests(ctx context.Context, run Run, work, serial, storage, loader, phase string, ranges []digestRange) ([]string, error) {
	ops := make([]operation, len(ranges))
	for i, r := range ranges {
		ops[i] = r.op
		ops[i].XMLName = xml.Name{Local: "getsha256digest"}
		ops[i].File = fmt.Sprintf("%s-%04d.sha256", phase, i)
		ops[i].FileOffset = 0
		ops[i].Sparse = ""
		if err := os.Remove(filepath.Join(work, ops[i].File)); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	path, err := writeXML(work, phase+".xml", ops)
	if err != nil {
		return nil, err
	}
	if err = run(ctx, work, serial, storage, loader, path); err != nil {
		return nil, fmt.Errorf("%s: %w (use readback verification if storage digests are unsupported)", phase, err)
	}
	values := make([]string, len(ops))
	for i, op := range ops {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		f, err := os.Open(filepath.Join(work, op.File))
		if err != nil {
			return nil, fmt.Errorf("missing storage digest: %w", err)
		}
		b, err := io.ReadAll(io.LimitReader(f, 65))
		f.Close()
		if err != nil {
			return nil, err
		}
		raw, err := hex.DecodeString(string(b))
		if err != nil || len(b) != 64 || len(raw) != sha256.Size {
			return nil, fmt.Errorf("invalid storage digest for LUN %d sector %d", op.LUN, op.Start)
		}
		values[i] = hex.EncodeToString(raw)
		if err = os.Remove(filepath.Join(work, op.File)); err != nil {
			return nil, err
		}
	}
	return values, nil
}

func flashDigest(ctx context.Context, writeRun, hashRun Run, dir, work, loader string, m Manifest, serial string, skip bool, hashes *imageHashes, result *Result) error {
	coarse := coarseDigests(dir, m)
	var programs []operation
	var written uint64
	total := manifestBytes(m)
	if skip {
		values, err := queryStorageDigests(ctx, hashRun, work, serial, m.Storage, loader, "digest-compare", coarse)
		if err != nil {
			return err
		}
		var fine []digestRange
		for i, r := range coarse {
			if values[i] != r.expected {
				fine = append(fine, hashes.fine[i]...)
			}
		}
		if len(fine) > 0 {
			values, err = queryStorageDigests(ctx, hashRun, work, serial, m.Storage, loader, "digest-refine", fine)
			if err != nil {
				return err
			}
			for i, r := range fine {
				if values[i] == r.expected {
					continue
				}
				op := r.op
				op.XMLName = xml.Name{Local: "program"}
				// Coalesce only within the same source file, preserving its offset.
				if len(programs) > 0 {
					last := &programs[len(programs)-1]
					if last.LUN == op.LUN && last.File == op.File && last.Start+last.Count == op.Start && last.FileOffset+last.Count == op.FileOffset {
						last.Count += op.Count
						continue
					}
				}
				programs = append(programs, op)
			}
		}
	} else {
		// Establish command support before the first write. The value is not used
		// to skip data; final complete-device verification is mandatory.
		probeRange := coarse[0]
		probeRange.op.Count = 2
		if _, err := queryStorageDigests(ctx, hashRun, work, serial, m.Storage, loader, "digest-probe", []digestRange{probeRange}); err != nil {
			return err
		}
		for _, r := range coarse {
			op := r.op
			op.XMLName = xml.Name{Local: "program"}
			programs = append(programs, op)
		}
	}
	for _, op := range programs {
		written += op.Count * op.Sector
	}
	result.BytesSkipped = total - written
	result.Verification = "digest"
	if skip {
		reportProgress(ctx, Progress{Serial: serial, Phase: "comparison result", Total: total, PlannedWrite: written, PlannedSkip: total - written, Verification: "digest"})
	}
	if len(programs) == 0 {
		return nil
	} // Coarse digests verified the complete image.
	path, err := writeXML(work, "restore.xml", programs)
	if err != nil {
		return err
	}
	if err = writeRun(ctx, work, serial, m.Storage, loader, path); err != nil {
		return fmt.Errorf("write (target may be partially written): %w", err)
	}
	result.BytesWritten = written
	values, err := queryStorageDigests(ctx, hashRun, work, serial, m.Storage, loader, "digest-verify", coarse)
	if err != nil {
		return err
	}
	for i, r := range coarse {
		if values[i] != r.expected {
			return fmt.Errorf("storage digest mismatch: LUN %d sector %d", r.op.LUN, r.op.Start)
		}
	}
	return nil
}
