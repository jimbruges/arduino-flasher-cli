// This file is part of arduino-flasher-cli.
//
// SPDX-FileCopyrightText: Arduino s.r.l. and/or its affiliated companies
// SPDX-License-Identifier: GPL-3.0-or-later

// Package golden captures and restores offline storage LUNs via QDL.
package golden

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/disk"

	"github.com/arduino/arduino-flasher-cli/internal/edl"
	"github.com/arduino/arduino-flasher-cli/internal/registry"
	"github.com/arduino/arduino-flasher-cli/internal/types/serial"
)

const chunkBytes = uint64(1 << 30)

type LUN struct {
	Number     int    `json:"number"`
	SectorSize uint64 `json:"sector_size"`
	Sectors    uint64 `json:"sectors"`
	Raw        bool   `json:"raw,omitempty"`
}
type Part struct {
	LUN     int    `json:"lun"`
	Start   uint64 `json:"start_sector"`
	Sectors uint64 `json:"sectors"`
	File    string `json:"file"`
	SHA256  string `json:"sha256"`
}
type Manifest struct {
	Version          int       `json:"version"`
	Board            string    `json:"board"`
	Storage          string    `json:"storage"`
	Source           string    `json:"source_serial"`
	Created          time.Time `json:"created"`
	ProgrammerSHA256 string    `json:"programmer_sha256"`
	LUNs             []LUN     `json:"luns"`
	Parts            []Part    `json:"parts"`
}

// Run executes QDL in a private working directory. Implementations must respect ctx.
type Run func(ctx context.Context, dir, serial, storage, programmer string, xmlPaths ...string) error

// CommandRunner retains a bounded diagnostic tail; large captures never buffer data in RAM.
func CommandRunner(qdl string) Run {
	return func(ctx context.Context, dir, serial, storage, programmer string, xmlPaths ...string) error {
		cmd := exec.CommandContext(ctx, qdl, append([]string{"--serial", serial, "--storage", storage, programmer}, xmlPaths...)...)
		cmd.Dir = dir
		tail := &tailWriter{}
		progress, err := newTransferProgress(ctx, dir, serial, xmlPaths)
		if err != nil {
			return err
		}
		output := io.MultiWriter(tail, progress)
		cmd.Stdout, cmd.Stderr = output, output
		progress.tick()
		stop, stopped := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(stopped)
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					progress.tick()
				}
			}
		}()
		err = cmd.Run()
		close(stop)
		<-stopped
		progress.finish(err)
		if err != nil {
			return fmt.Errorf("qdl: %w: %s", err, tail.data)
		}
		return nil
	}
}

type tailWriter struct{ data []byte }

func (w *tailWriter) Write(p []byte) (int, error) {
	n := len(p)
	w.data = append(w.data, p...)
	if len(w.data) > 8192 {
		w.data = append([]byte(nil), w.data[len(w.data)-8192:]...)
	}
	return n, nil
}

// NormalizeSerial follows the CLI convention: decimal unless prefixed with 0x or containing hex letters.
func NormalizeSerial(s string) (string, error) {
	v, err := serial.Parse(s)
	if err != nil || strings.HasPrefix(v.Decimal(), "-") || s == "" {
		return "", fmt.Errorf("invalid serial %q", s)
	}
	return v.Hex(), nil
}

func validateConfig(board, storage string, luns []LUN) error {
	if _, ok := registry.BoardByID(board); !ok {
		return fmt.Errorf("unknown board %q", board)
	}
	if storage != "emmc" && storage != "ufs" {
		return errors.New("storage must be emmc or ufs")
	}
	if len(luns) == 0 || len(luns) > 8 {
		return errors.New("select between one and eight storage LUNs")
	}
	seen := map[int]bool{}
	for _, l := range luns {
		if l.Number < 0 || l.Number > 7 || seen[l.Number] || (l.SectorSize != 512 && l.SectorSize != 4096) {
			return errors.New("invalid or duplicate LUN, or unsupported sector size")
		}
		if l.Raw && (l.Sectors < 2 || l.Sectors > (1<<63-1)/l.SectorSize) {
			return errors.New("raw LUN needs a valid explicit sector count")
		}
		seen[l.Number] = true
	}
	return nil
}

// GPTCapacity validates the primary GPT header and obtains the actual disk extent
// from its backup-header LBA, not the last usable partition LBA.
func GPTCapacity(data []byte, sector uint64) (uint64, error) {
	if (sector != 512 && sector != 4096) || len(data) != int(2*sector) {
		return 0, errors.New("short GPT read or invalid sector size")
	}
	h := append([]byte(nil), data[sector:]...)
	if string(h[:8]) != "EFI PART" {
		return 0, errors.New("LUN has no GPT; use --raw-lun with its documented physical size")
	}
	size := binary.LittleEndian.Uint32(h[12:16])
	crc := binary.LittleEndian.Uint32(h[16:20])
	if size < 92 || uint64(size) > sector {
		return 0, errors.New("invalid GPT header length")
	}
	clear(h[16:20])
	if crc32.ChecksumIEEE(h[:size]) != crc || binary.LittleEndian.Uint64(h[24:32]) != 1 {
		return 0, errors.New("invalid GPT header CRC or location")
	}
	last := binary.LittleEndian.Uint64(h[32:40])
	if last < 3 || last > (1<<63-1)/sector-1 || binary.LittleEndian.Uint64(h[48:56]) >= last {
		return 0, errors.New("invalid GPT disk extent")
	}
	return last + 1, nil
}

type operation struct {
	XMLName    xml.Name
	Sector     uint64 `xml:"SECTOR_SIZE_IN_BYTES,attr"`
	File       string `xml:"filename,attr"`
	LUN        int    `xml:"physical_partition_number,attr"`
	Count      uint64 `xml:"num_partition_sectors,attr"`
	Start      uint64 `xml:"start_sector,attr"`
	Sparse     string `xml:"sparse,attr,omitempty"`
	Label      string `xml:"label,attr,omitempty"`
	FileOffset uint64 `xml:"file_sector_offset,attr"`
}

func writeXML(dir, name string, ops []operation) (string, error) {
	for i := range ops {
		if ops[i].XMLName.Local == "program" {
			ops[i].Label = filepath.Base(ops[i].File)
		}
	}
	data, err := xml.Marshal(struct {
		XMLName xml.Name    `xml:"data"`
		Ops     []operation `xml:",any"`
	}{Ops: ops})
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, name)
	return p, os.WriteFile(p, data, 0600)
}
func probe(ctx context.Context, run Run, dir, serial, storage, programmer string, luns []LUN) ([]LUN, error) {
	ops := []operation{}
	for _, l := range luns {
		start := uint64(0)
		if l.Raw {
			start = l.Sectors - 2
		}
		ops = append(ops, operation{XMLName: xml.Name{Local: "read"}, Sector: l.SectorSize, File: fmt.Sprintf("probe-%d.bin", l.Number), LUN: l.Number, Count: 2, Start: start})
	}
	p, err := writeXML(dir, "probe.xml", ops)
	if err != nil {
		return nil, err
	}
	if err = run(ctx, dir, serial, storage, programmer, p); err != nil {
		return nil, err
	}
	out := append([]LUN(nil), luns...)
	for i, l := range out {
		b, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("probe-%d.bin", l.Number)))
		if err != nil {
			return nil, err
		}
		if l.Raw {
			if uint64(len(b)) != 2*l.SectorSize {
				return nil, fmt.Errorf("short raw LUN %d probe", l.Number)
			}
			continue
		}
		n, err := GPTCapacity(b, l.SectorSize)
		if err != nil {
			return nil, fmt.Errorf("LUN %d: %w", l.Number, err)
		}
		out[i].Sectors = n
	}
	return out, nil
}
func digest(ctx context.Context, path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	if !st.Mode().IsRegular() {
		return "", 0, errors.New("image is not a regular file")
	}
	h := sha256.New()
	buf := make([]byte, 1024*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}
		n, err := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			total += int64(n)
			advanceProgress(ctx, uint64(n))
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", 0, err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), total, nil
}
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	return errors.Join(err, out.Close())
}

// Capture publishes manifest.json only after every captured byte has been hashed.
// output must not exist. An interrupted capture leaves no apparently complete bundle.
func Capture(ctx context.Context, run Run, board, storage, source, programmer, output string, luns []LUN) (retErr error) {
	if err := validateConfig(board, storage, luns); err != nil {
		return err
	}
	s, err := NormalizeSerial(source)
	if err != nil {
		return err
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return err
	}
	if err = os.Mkdir(output, 0700); err != nil {
		return err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(output)
		}
	}()
	loader := filepath.Join(output, "programmer.elf")
	if err = copyFile(programmer, loader); err != nil {
		return err
	}
	sha, _, err := digest(ctx, loader)
	if err != nil {
		return err
	}
	geometry, err := probe(ctx, run, output, s, storage, loader, luns)
	if err != nil {
		return err
	}
	m := Manifest{Version: 1, Board: board, Storage: storage, Source: s, Created: time.Now().UTC(), ProgrammerSHA256: sha, LUNs: geometry}
	if err := checkSpace(output, geometry, 1); err != nil {
		return err
	}
	var ops []operation
	for _, l := range geometry {
		for start := uint64(0); start < l.Sectors; {
			count := min(chunkBytes/l.SectorSize, l.Sectors-start)
			name := fmt.Sprintf("lun%d-%012d.bin", l.Number, start)
			m.Parts = append(m.Parts, Part{LUN: l.Number, Start: start, Sectors: count, File: name})
			ops = append(ops, operation{XMLName: xml.Name{Local: "read"}, Sector: l.SectorSize, File: name, LUN: l.Number, Count: count, Start: start})
			start += count
		}
	}
	p, err := writeXML(output, "capture.xml", ops)
	if err != nil {
		return err
	}
	if err = run(ctx, output, s, storage, loader, p); err != nil {
		return err
	}
	ctx, hashing := beginProgress(ctx, s, "capture checksums", manifestBytes(m))
	defer func() { hashing.finish(retErr) }()
	for i, part := range m.Parts {
		hash, n, err := digest(ctx, filepath.Join(output, part.File))
		if err != nil {
			return err
		}
		if uint64(n) != part.Sectors*sectorFor(m, part.LUN) {
			return fmt.Errorf("short read of %s", part.File)
		}
		m.Parts[i].SHA256 = hash
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(output, "manifest.json"), b, 0600); err != nil {
		return err
	}
	complete = true
	return nil
}
func sectorFor(m Manifest, lun int) uint64 {
	for _, l := range m.LUNs {
		if l.Number == lun {
			return l.SectorSize
		}
	}
	return 0
}

// Load verifies the manifest's complete, non-overlapping coverage and all file hashes before any board is touched.
func Load(ctx context.Context, dir string) (m Manifest, retErr error) {
	f, err := os.Open(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return m, err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 16<<20))
	d.DisallowUnknownFields()
	if err = d.Decode(&m); err != nil {
		return m, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return m, errors.New("unexpected trailing manifest data")
	}
	if m.Version != 1 {
		return m, errors.New("unsupported golden image version")
	}
	if err = validateConfig(m.Board, m.Storage, m.LUNs); err != nil {
		return m, err
	}
	// Stored serials are canonical hexadecimal, even when they contain only digits.
	s, err := NormalizeSerial("0x" + m.Source)
	if err != nil || s != m.Source {
		return m, errors.New("invalid source serial")
	}
	next := map[int]uint64{}
	sizes := map[int]uint64{}
	files := map[string]bool{}
	for _, l := range m.LUNs {
		if l.Sectors < 2 || (!l.Raw && l.Sectors < 4) || l.Sectors > (1<<63-1)/l.SectorSize {
			return m, errors.New("invalid LUN size")
		}
		sizes[l.Number] = l.Sectors
	}
	for _, p := range m.Parts {
		sector := sectorFor(m, p.LUN)
		if sector == 0 || p.Start != next[p.LUN] || p.Sectors == 0 || p.Sectors > chunkBytes/sector || p.Start > sizes[p.LUN] || p.Sectors > sizes[p.LUN]-p.Start {
			return m, errors.New("invalid, overlapping or incomplete image extent")
		}
		if p.File != fmt.Sprintf("lun%d-%012d.bin", p.LUN, p.Start) || files[p.File] {
			return m, errors.New("invalid image filename")
		}
		files[p.File] = true
		next[p.LUN] += p.Sectors
	}
	for lun, n := range sizes {
		if next[lun] != n {
			return m, errors.New("image does not cover the entire LUN")
		}
	}
	hashCtx, hashing := beginProgress(ctx, "", "image checksums", manifestBytes(m))
	defer func() { hashing.finish(retErr) }()
	for _, p := range m.Parts {
		hash, n, err := digest(hashCtx, filepath.Join(dir, p.File))
		if err != nil {
			return m, err
		}
		if hash != p.SHA256 || uint64(n) != p.Sectors*sectorFor(m, p.LUN) {
			return m, fmt.Errorf("checksum or size mismatch: %s", p.File)
		}
	}
	hash, _, err := digest(ctx, filepath.Join(dir, "programmer.elf"))
	if err != nil {
		return m, err
	}
	if hash != m.ProgrammerSHA256 {
		return m, errors.New("programmer checksum mismatch")
	}
	return m, nil
}

type Result struct {
	Serial          string  `json:"serial"`
	Status          string  `json:"status"`
	Error           string  `json:"error,omitempty"`
	DurationSeconds float64 `json:"duration_seconds,omitempty"`
	Verification    string  `json:"verification,omitempty"`
	BytesWritten    uint64  `json:"bytes_written,omitempty"`
	BytesSkipped    uint64  `json:"bytes_skipped,omitempty"`
}
type FlashOptions struct {
	Board   string
	Serials []string
	Jobs    int
	TempDir string
	Timeout time.Duration
	// DryRun validates and reports the plan without opening USB devices.
	DryRun bool
	// RestoreSource permits restoring only the source board, as a single-target recovery.
	RestoreSource bool
	// All snapshots connected EDL devices once and excludes the source.
	All bool
	// SkipUnchanged compares the target first and writes only differing regions.
	SkipUnchanged bool
	// Verification selects "readback" (default) or "digest".
	Verification string
	// DigestRun is a QDL runner with explicit getsha256digest XML support.
	DigestRun Run
	// DigestBlockBytes bounds changed regions; default 64 MiB.
	DigestBlockBytes uint64
	// Discover overrides the native USB inventory for embedding and tests.
	Discover func(context.Context) ([]edl.Device, error)
	// OnResult runs once per finished target, after QDL and scratch cleanup. Calls
	// are serialized; callbacks should return promptly. Dry-runs do not call it.
	OnResult func(Result)
}

// Flash uses explicit targets only, rejects the source by default and duplicate serials, and
// attempts every target even if another fails. Each worker probes before writing.
func Flash(ctx context.Context, run Run, dir string, opts FlashOptions) ([]Result, error) {
	if opts.Verification == "" {
		opts.Verification = "readback"
	}
	if opts.Verification != "readback" && opts.Verification != "digest" {
		return nil, errors.New("verification must be readback or digest")
	}
	if opts.DigestBlockBytes == 0 {
		opts.DigestBlockBytes = 64 << 20
	}
	if opts.DigestBlockBytes < 1<<20 || opts.DigestBlockBytes > chunkBytes || opts.DigestBlockBytes&(opts.DigestBlockBytes-1) != 0 {
		return nil, errors.New("digest block size must be a power of two from 1 MiB to 1 GiB")
	}
	if opts.Verification == "digest" && opts.DigestRun == nil && !opts.DryRun {
		return nil, errors.New("digest verification requires a digest-capable QDL runner")
	}
	if opts.Jobs < 1 || opts.Jobs > 32 || opts.Timeout <= 0 || (!opts.All && len(opts.Serials) == 0) {
		return nil, errors.New("provide target serials, jobs 1..32 and a positive timeout")
	}
	if opts.All && (len(opts.Serials) > 0 || opts.RestoreSource) {
		return nil, errors.New("--all cannot be combined with --serial or --restore-source")
	}
	if opts.RestoreSource && len(opts.Serials) != 1 {
		return nil, errors.New("source recovery requires exactly one target")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	m, err := Load(ctx, dir)
	if err != nil {
		return nil, err
	}
	if opts.Board != m.Board {
		return nil, fmt.Errorf("image is for %s, not %s", m.Board, opts.Board)
	}
	targets := opts.Serials
	if opts.All {
		discover := opts.Discover
		if discover == nil {
			discover = edl.Discover
		}
		devices, err := discover(ctx)
		if err != nil {
			return nil, err
		}
		targets = make([]string, 0, len(devices))
		for _, d := range devices {
			s, err := NormalizeSerial(d.Serial)
			if err != nil {
				return nil, err
			}
			if s != m.Source {
				targets = append(targets, "0x"+s)
			}
		}
		if len(targets) == 0 {
			return nil, errors.New("no EDL targets found after excluding the source; connect targets in EDL mode")
		}
	}
	results := make([]Result, len(targets))
	seen := map[string]bool{}
	for i, v := range targets {
		s, err := NormalizeSerial(v)
		if err != nil {
			return nil, err
		}
		if opts.RestoreSource && s != m.Source {
			return nil, errors.New("source recovery target must match the image source")
		}
		if (s == m.Source && !opts.RestoreSource) || seen[s] {
			return nil, fmt.Errorf("source or duplicate target serial: %s", v)
		}
		seen[s] = true
		results[i] = Result{Serial: s, Status: "planned", Verification: opts.Verification}
	}
	if opts.DryRun {
		return results, nil
	}
	tempParent := opts.TempDir
	if tempParent == "" {
		tempParent = os.TempDir()
	}
	if opts.Verification == "readback" {
		if err := checkSpace(tempParent, m.LUNs, min(opts.Jobs, len(results))); err != nil {
			return nil, err
		}
	}
	var hashes *imageHashes
	if opts.Verification == "digest" && opts.SkipUnchanged {
		hashes, err = prepareImageHashes(ctx, dir, m, opts.DigestBlockBytes)
		if err != nil {
			return nil, err
		}
	}
	for _, r := range results {
		reportProgress(ctx, Progress{Serial: r.Serial, Phase: "queued"})
	}
	work := make(chan int)
	var wg sync.WaitGroup
	var resultMu sync.Mutex
	for w := 0; w < min(opts.Jobs, len(results)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				started := time.Now()
				targetCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
				err := flashOne(targetCtx, run, dir, m, results[i].Serial, opts.TempDir, opts, hashes, &results[i])
				cancel()
				results[i].DurationSeconds = time.Since(started).Seconds()
				if err != nil {
					results[i].Status = "failed"
					results[i].Error = err.Error()
				} else {
					results[i].Status = "verified"
				}
				if opts.OnResult != nil {
					resultMu.Lock()
					opts.OnResult(results[i])
					resultMu.Unlock()
				}
			}
		}()
	}
	for i := range results {
		work <- i
	}
	close(work)
	wg.Wait()
	var failures []error
	for _, r := range results {
		if r.Status == "failed" {
			failures = append(failures, fmt.Errorf("%s: %s", r.Serial, r.Error))
		}
	}
	return results, errors.Join(failures...)
}
func flashOne(ctx context.Context, run Run, dir string, m Manifest, s, temp string, opts FlashOptions, hashes *imageHashes, result *Result) (retErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	work, err := os.MkdirTemp(temp, "golden-"+s+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	work, err = filepath.Abs(work)
	if err != nil {
		return err
	}
	loader := filepath.Join(dir, "programmer.elf")
	geometry, err := probe(ctx, run, work, s, m.Storage, loader, m.LUNs)
	if err != nil {
		return fmt.Errorf("preflight: %w", err)
	}
	for i, l := range geometry {
		if l != m.LUNs[i] {
			return fmt.Errorf("LUN %d geometry differs from source; same-sized storage required", l.Number)
		}
	}
	if opts.Verification == "digest" {
		return flashDigest(ctx, run, opts.DigestRun, dir, work, loader, m, s, opts.SkipUnchanged, hashes, result)
	}
	result.Verification = "readback"
	var programs, reads []operation
	var total, written uint64
	for _, p := range m.Parts {
		programs = append(programs, operation{XMLName: xml.Name{Local: "program"}, Sector: sectorFor(m, p.LUN), File: filepath.Join(dir, p.File), LUN: p.LUN, Count: p.Sectors, Start: p.Start, Sparse: "false"})
		reads = append(reads, operation{XMLName: xml.Name{Local: "read"}, Sector: sectorFor(m, p.LUN), File: p.File, LUN: p.LUN, Count: p.Sectors, Start: p.Start})
		total += p.Sectors * sectorFor(m, p.LUN)
	}
	if opts.SkipUnchanged {
		compareXML, err := writeXML(work, "compare.xml", reads)
		if err != nil {
			return err
		}
		if err = run(ctx, work, s, m.Storage, loader, compareXML); err != nil {
			return fmt.Errorf("read-before-write: %w", err)
		}
		programs, err = compareImage(ctx, dir, work, s, m)
		if err != nil {
			return err
		}
		// Avoid counting stale pre-read data as fresh verification progress.
		for _, p := range m.Parts {
			if err := os.Remove(filepath.Join(work, p.File)); err != nil {
				return err
			}
		}
	}
	for _, op := range programs {
		written += op.Count * op.Sector
	}
	result.BytesSkipped = total - written
	if opts.SkipUnchanged {
		reportProgress(ctx, Progress{Serial: s, Phase: "comparison result", Total: total, PlannedWrite: written, PlannedSkip: total - written})
	}
	if len(programs) == 0 {
		return nil
	} // The full comparison already verified every byte.
	p, err := writeXML(work, "restore.xml", programs)
	if err != nil {
		return err
	}
	readXML, err := writeXML(work, "verify.xml", reads)
	if err != nil {
		return err
	}
	// QDL writes all program ranges, then rereads the entire image before reset.
	if err = run(ctx, work, s, m.Storage, loader, p, readXML); err != nil {
		return fmt.Errorf("write/readback (target may be partially written): %w", err)
	}
	result.BytesWritten = written
	ctx, hashing := beginProgress(ctx, s, "verify checksums", total)
	defer func() { hashing.finish(retErr) }()
	for _, part := range m.Parts {
		hash, n, err := digest(ctx, filepath.Join(work, part.File))
		if err != nil {
			return err
		}
		if hash != part.SHA256 || uint64(n) != part.Sectors*sectorFor(m, part.LUN) {
			return fmt.Errorf("readback mismatch: %s", part.File)
		}
	}
	return nil
}

// ParseLUNs accepts comma-separated physical LUN numbers; geometry is read from GPT.
func ParseLUNs(s string, sector uint64) ([]LUN, error) {
	var out []LUN
	if strings.TrimSpace(s) == "" {
		return out, nil
	}
	for _, v := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return nil, err
		}
		out = append(out, LUN{Number: n, SectorSize: sector})
	}
	return out, nil
}

// ParseRawLUN describes a non-GPT boot LUN using its documented physical size.
func ParseRawLUN(value string, sector uint64) (LUN, error) {
	fields := strings.Split(value, ":")
	if len(fields) != 2 {
		return LUN{}, errors.New("raw LUN must be NUMBER:SECTORS")
	}
	number, err := strconv.Atoi(fields[0])
	if err != nil {
		return LUN{}, err
	}
	n, err := strconv.ParseUint(fields[1], 10, 64)
	return LUN{Number: number, SectorSize: sector, Sectors: n, Raw: true}, err
}
func checkSpace(dir string, luns []LUN, copies int) error {
	var total uint64
	for _, l := range luns {
		n := l.Sectors * l.SectorSize
		if n > uint64(1<<63-1)-total {
			return errors.New("image size overflow")
		}
		total += n
	}
	if total > uint64(1<<63-1)/uint64(copies) {
		return errors.New("readback space overflow")
	}
	usage, err := disk.Usage(dir)
	if err != nil {
		return err
	}
	if usage.Free < total*uint64(copies) {
		return fmt.Errorf("%s needs %d bytes free for capture/readback, has %d", dir, total*uint64(copies), usage.Free)
	}
	return nil
}
