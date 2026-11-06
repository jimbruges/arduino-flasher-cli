// This file is part of arduino-flasher-cli.
//
// SPDX-FileCopyrightText: Arduino s.r.l. and/or its affiliated companies
// SPDX-License-Identifier: GPL-3.0-or-later

package golden

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"hash/crc32"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arduino/arduino-flasher-cli/internal/edl"
)

func diskImage(sector, n uint64) []byte {
	b := bytes.Repeat([]byte{0x37}, int(sector*n))
	h := b[sector : 2*sector]
	clear(h)
	copy(h, "EFI PART")
	binary.LittleEndian.PutUint32(h[12:], 92)
	binary.LittleEndian.PutUint64(h[24:], 1)
	binary.LittleEndian.PutUint64(h[32:], n-1)
	binary.LittleEndian.PutUint64(h[48:], n-3)
	binary.LittleEndian.PutUint32(h[16:], crc32.ChecksumIEEE(h[:92]))
	return b
}
func TestGPTCapacity(t *testing.T) {
	for _, sector := range []uint64{512, 4096} {
		b := diskImage(sector, 32)
		n, err := GPTCapacity(b[:2*sector], sector)
		if err != nil || n != 32 {
			t.Fatalf("%d: %d %v", sector, n, err)
		}
		b[sector+48] ^= 1
		if _, err = GPTCapacity(b[:2*sector], sector); err == nil {
			t.Fatal("accepted corrupt GPT")
		}
	}
	for _, b := range [][]byte{nil, make([]byte, 1024), make([]byte, 9000)} {
		if _, err := GPTCapacity(b, 512); err == nil {
			t.Fatal("accepted malformed GPT")
		}
	}
}

// A byte-addressed fake Firehose device exercises the actual generated read/program XML.
type fakeDevice struct {
	mu      sync.Mutex
	disks   map[string]map[int][]byte
	calls   int
	writes  int
	corrupt string
}

func (f *fakeDevice) run(ctx context.Context, dir, s, storage, loader string, xmlPaths ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	for _, path := range xmlPaths {
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var doc struct {
			Ops []operation `xml:",any"`
		}
		if err = xml.Unmarshal(b, &doc); err != nil {
			return err
		}
		for _, op := range doc.Ops {
			disk := f.disks[s][op.LUN]
			start, end := op.Start*op.Sector, (op.Start+op.Count)*op.Sector
			if end > uint64(len(disk)) {
				return errors.New("out of bounds")
			}
			p := op.File
			if !filepath.IsAbs(p) {
				p = filepath.Join(dir, p)
			}
			switch op.XMLName.Local {
			case "program":
				data, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				offset := op.FileOffset * op.Sector
				if uint64(len(data)) < offset+end-start {
					return errors.New("wrong program size")
				}
				copy(disk[start:end], data[offset:offset+end-start])
				f.writes++
			case "getsha256digest":
				data := append([]byte(nil), disk[start:end]...)
				if s == f.corrupt && filepath.Base(path) == "digest-verify.xml" {
					data[0] ^= 1
				}
				hash := sha256.Sum256(data)
				if err := os.WriteFile(p, []byte(hex.EncodeToString(hash[:])), 0600); err != nil {
					return err
				}
			case "read":
				data := append([]byte(nil), disk[start:end]...)
				if s == f.corrupt && filepath.Base(path) == "verify.xml" {
					data[0] ^= 1
				}
				if err := os.WriteFile(p, data, 0600); err != nil {
					return err
				}
			default:
				return errors.New("unknown operation")
			}
		}
	}
	return nil
}
func fixture(t *testing.T, board string, sector uint64) (string, *fakeDevice) {
	t.Helper()
	root := t.TempDir()
	loader := filepath.Join(root, "loader.elf")
	if err := os.WriteFile(loader, []byte("fake loader"), 0600); err != nil {
		t.Fatal(err)
	}
	f := &fakeDevice{disks: map[string]map[int][]byte{"00000001": {0: diskImage(sector, 32), 1: bytes.Repeat([]byte{0x42}, int(4*sector))}}}
	dir := filepath.Join(root, "bundle")
	storage := "emmc"
	if sector == 4096 {
		storage = "ufs"
	}
	err := Capture(context.Background(), f.run, board, storage, "1", loader, dir, []LUN{{Number: 0, SectorSize: sector}, {Number: 1, SectorSize: sector, Sectors: 4, Raw: true}})
	if err != nil {
		t.Fatal(err)
	}
	return dir, f
}
func options(board string, serials ...string) FlashOptions {
	return FlashOptions{Board: board, Serials: serials, Jobs: 2, Timeout: time.Second * 5}
}
func TestCaptureRestoreBothBoards(t *testing.T) {
	for _, v := range []struct {
		board  string
		sector uint64
	}{{"unoq", 512}, {"ventunoq", 4096}} {
		t.Run(v.board, func(t *testing.T) {
			dir, f := fixture(t, v.board, v.sector)
			f.disks["00000002"] = map[int][]byte{0: diskImage(v.sector, 32), 1: make([]byte, 4*v.sector)}
			f.disks["00000003"] = map[int][]byte{0: diskImage(v.sector, 32), 1: make([]byte, 4*v.sector)}
			results, err := Flash(context.Background(), f.run, dir, options(v.board, "2", "3"))
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range results {
				if r.Status != "verified" {
					t.Fatal(r)
				}
				for lun, want := range f.disks["00000001"] {
					if !bytes.Equal(want, f.disks[r.Serial][lun]) {
						t.Fatal("restored bytes differ")
					}
				}
			}
		})
	}
}
func TestSelectionAndCorruptionNeverTouchUSB(t *testing.T) {
	dir, f := fixture(t, "unoq", 512)
	initial := f.calls
	for _, opts := range []FlashOptions{options("unoq", "1"), options("unoq", "2", "0x2"), options("ventunoq", "2"), options("unoq", "-1"), options("unoq")} {
		if _, err := Flash(context.Background(), f.run, dir, opts); err == nil {
			t.Fatal("accepted invalid selection")
		}
	}
	dry := options("unoq", "2")
	dry.DryRun = true
	if _, err := Flash(context.Background(), nil, dir, dry); err != nil {
		t.Fatal(err)
	}
	m, err := Load(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, m.Parts[0].File), []byte("corrupted"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Flash(context.Background(), f.run, dir, options("unoq", "2")); err == nil {
		t.Fatal("accepted corruption")
	}
	if f.calls != initial {
		t.Fatal("invalid request touched USB")
	}
}
func TestTargetFailureIsolationAndVerification(t *testing.T) {
	dir, f := fixture(t, "unoq", 512)
	f.disks["00000002"] = map[int][]byte{0: diskImage(512, 64), 1: make([]byte, 2048)}
	f.disks["00000003"] = map[int][]byte{0: diskImage(512, 32), 1: make([]byte, 2048)}
	results, err := Flash(context.Background(), f.run, dir, options("unoq", "2", "3"))
	if err == nil || results[0].Status != "failed" || results[1].Status != "verified" || f.writes != 2 {
		t.Fatalf("%v %v writes=%d", results, err, f.writes)
	}
	f.corrupt = "00000003"
	if _, err = Flash(context.Background(), f.run, dir, options("unoq", "3")); err == nil {
		t.Fatal("readback corruption accepted")
	}
}
func TestConcurrencyBoundAndCancellation(t *testing.T) {
	dir, _ := fixture(t, "unoq", 512)
	var active, peak atomic.Int32
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	run := func(ctx context.Context, _, _, _, _ string, _ ...string) error {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		arrived <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return errors.New("simulated failure")
	}
	done := make(chan error, 1)
	go func() { _, err := Flash(context.Background(), run, dir, options("unoq", "2", "3", "4")); done <- err }()
	for i := 0; i < 2; i++ {
		select {
		case <-arrived:
		case <-time.After(time.Second * 5):
			t.Fatal("workers did not run concurrently")
		}
	}
	close(release)
	if err := <-done; err == nil || peak.Load() != 2 {
		t.Fatalf("peak=%d err=%v", peak.Load(), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Flash(ctx, run, dir, options("unoq", "2")); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
}
func TestFailedCaptureCleanup(t *testing.T) {
	root := t.TempDir()
	loader := filepath.Join(root, "loader")
	_ = os.WriteFile(loader, []byte("x"), 0600)
	dir := filepath.Join(root, "bundle")
	run := func(context.Context, string, string, string, string, ...string) error {
		return errors.New("disconnected")
	}
	if err := Capture(context.Background(), run, "unoq", "emmc", "1", loader, dir, []LUN{{SectorSize: 512}}); err == nil {
		t.Fatal("capture succeeded")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("partial bundle left behind")
	}
	_ = os.Mkdir(dir, 0700)
	sentinel := filepath.Join(dir, "keep")
	_ = os.WriteFile(sentinel, []byte("keep"), 0600)
	_ = Capture(context.Background(), run, "unoq", "emmc", "1", loader, dir, []LUN{{SectorSize: 512}})
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatal("existing directory changed")
	}
}
func TestMalformedManifest(t *testing.T) {
	dir, _ := fixture(t, "unoq", 512)
	original, err := Load(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Manifest){
		func(m *Manifest) { m.Parts[0].File = "../outside" },
		func(m *Manifest) { m.Parts[0].Start++ },
		func(m *Manifest) { m.Parts = m.Parts[:1] },
		func(m *Manifest) { m.LUNs[0].Sectors = ^uint64(0) },
		func(m *Manifest) { m.ProgrammerSHA256 = "bad" },
	} {
		b, _ := json.Marshal(original)
		var m Manifest
		_ = json.Unmarshal(b, &m)
		mutate(&m)
		b, _ = json.Marshal(m)
		_ = os.WriteFile(filepath.Join(dir, "manifest.json"), b, 0600)
		if _, err := Load(context.Background(), dir); err == nil {
			t.Fatal("accepted malformed manifest")
		}
	}
}

func TestTargetTimeoutDoesNotBlockQueue(t *testing.T) {
	dir, _ := fixture(t, "unoq", 512)
	var calls atomic.Int32
	run := func(ctx context.Context, _, _, _, _ string, _ ...string) error {
		calls.Add(1)
		<-ctx.Done()
		return ctx.Err()
	}
	opts := options("unoq", "2", "3")
	opts.Jobs = 1
	opts.Timeout = 10 * time.Millisecond
	results, err := Flash(context.Background(), run, dir, opts)
	if err == nil || calls.Load() != 2 || len(results) != 2 || results[0].Status != "failed" || results[1].Status != "failed" {
		t.Fatalf("%v %v calls=%d", results, err, calls.Load())
	}
}

// Enable with GOLDEN_TEST_QDL=/absolute/path/to/qdl to check the real bundled
// parser and program/read ordering without opening USB devices.
func TestQDLCompatibility(t *testing.T) {
	qdl := os.Getenv("GOLDEN_TEST_QDL")
	if qdl == "" {
		t.Skip("GOLDEN_TEST_QDL is not set")
	}
	dir, _ := fixture(t, "unoq", 512)
	m, err := Load(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	var ops []operation
	for _, p := range m.Parts {
		ops = append(ops, operation{XMLName: xml.Name{Local: "program"}, Sector: 512, File: filepath.Join(dir, p.File), LUN: p.LUN, Count: p.Sectors, Start: p.Start, Sparse: "false"})
		ops = append(ops, operation{XMLName: xml.Name{Local: "read"}, Sector: 512, File: p.File, LUN: p.LUN, Count: p.Sectors, Start: p.Start})
	}
	var programs, reads []operation
	for _, op := range ops {
		if op.XMLName.Local == "program" {
			programs = append(programs, op)
		} else {
			reads = append(reads, op)
		}
	}
	path, err := writeXML(work, "restore.xml", programs)
	if err != nil {
		t.Fatal(err)
	}
	readXML, err := writeXML(work, "verify.xml", reads)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, qdl, "--dry-run", "--serial", "00000002", "--storage", "emmc", filepath.Join(dir, "programmer.elf"), path, readXML)
	cmd.Dir = work
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("QDL rejected generated XML: %v\n%s", err, output)
	}
}

func TestSourceRecoveryIsExplicit(t *testing.T) {
	dir, f := fixture(t, "unoq", 512)
	initial := f.calls
	for _, targets := range [][]string{{"1", "0x1"}} {
		opts := options("unoq", targets...)
		opts.RestoreSource = true
		if _, err := Flash(context.Background(), f.run, dir, opts); err == nil {
			t.Fatalf("accepted source recovery targets %v", targets)
		}
	}
	if f.calls != initial {
		t.Fatal("invalid recovery touched USB")
	}
	f.disks["00000001"][0][0] ^= 1
	opts := options("unoq", "0x1")
	opts.RestoreSource = true
	results, err := Flash(context.Background(), f.run, dir, opts)
	if err != nil || results[0].Status != "verified" || results[0].DurationSeconds <= 0 {
		t.Fatalf("%v %v", results, err)
	}
	if f.disks["00000001"][0][0] != 0x37 || f.writes != 2 {
		t.Fatal("source not restored")
	}
}

func TestAutoDiscoverySnapshotsAndExcludesSource(t *testing.T) {
	dir, f := fixture(t, "unoq", 512)
	for _, s := range []string{"11111111", "22222222"} {
		f.disks[s] = map[int][]byte{0: diskImage(512, 32), 1: make([]byte, 2048)}
	}
	calls := 0
	opts := options("unoq")
	opts.All = true
	opts.Discover = func(context.Context) ([]edl.Device, error) {
		calls++
		return []edl.Device{{Serial: "0x00000001"}, {Serial: "0x11111111"}, {Serial: "0x22222222"}}, nil
	}
	result, err := Flash(context.Background(), f.run, dir, opts)
	if err != nil || len(result) != 2 || result[0].Serial != "11111111" || result[1].Serial != "22222222" || calls != 1 || f.writes != 4 {
		t.Fatalf("%v %v calls=%d writes=%d", result, err, calls, f.writes)
	}
	dry := opts
	dry.DryRun = true
	initial := f.calls
	if _, err := Flash(context.Background(), f.run, dir, dry); err != nil || f.calls != initial {
		t.Fatalf("dry-run touched USB: %v", err)
	}
}
func TestAutoDiscoveryFailuresNeverWrite(t *testing.T) {
	dir, f := fixture(t, "unoq", 512)
	initial := f.calls
	for _, devices := range [][]edl.Device{nil, {{Serial: "0x00000001"}}, {{Serial: "0x11111111"}, {Serial: "0x11111111"}}, {{Serial: "invalid"}}} {
		opts := options("unoq")
		opts.All = true
		opts.Discover = func(context.Context) ([]edl.Device, error) { return devices, nil }
		if _, err := Flash(context.Background(), f.run, dir, opts); err == nil {
			t.Fatalf("accepted targets %v", devices)
		}
	}
	for _, opts := range []FlashOptions{
		{Board: "unoq", Jobs: 1, Timeout: time.Second, All: true, Serials: []string{"2"}},
		{Board: "unoq", Jobs: 1, Timeout: time.Second, All: true, RestoreSource: true},
	} {
		if _, err := Flash(context.Background(), f.run, dir, opts); err == nil {
			t.Fatal("accepted conflicting target selection")
		}
	}
	opts := options("unoq")
	opts.All = true
	opts.Discover = func(context.Context) ([]edl.Device, error) { return nil, errors.New("USB inventory failed") }
	if _, err := Flash(context.Background(), f.run, dir, opts); err == nil {
		t.Fatal("ignored inventory failure")
	}
	if f.calls != initial {
		t.Fatal("invalid auto selection touched USB")
	}
}

func TestSkipUnchangedVerifiesWithoutWriting(t *testing.T) {
	dir, f := fixture(t, "unoq", 512)
	opts := options("unoq", "1")
	opts.RestoreSource = true
	opts.SkipUnchanged = true
	var plan Progress
	ctx := WithProgress(context.Background(), func(p Progress) {
		if p.Phase == "comparison result" {
			plan = p
		}
	})
	r, err := Flash(ctx, f.run, dir, opts)
	if err != nil || r[0].Status != "verified" || f.writes != 0 || r[0].BytesWritten != 0 || r[0].BytesSkipped != 36*512 {
		t.Fatalf("%v %v writes=%d", r, err, f.writes)
	}
	if plan.PlannedWrite != 0 || plan.PlannedSkip != 36*512 {
		t.Fatalf("incorrect unchanged write plan: %+v", plan)
	}
	f.disks["00000001"][1][0] ^= 1
	r, err = Flash(ctx, f.run, dir, opts)
	if err != nil || f.writes != 1 || r[0].BytesWritten != 4*512 || r[0].BytesSkipped != 32*512 {
		t.Fatalf("%v %v writes=%d", r, err, f.writes)
	}
	if plan.PlannedWrite != r[0].BytesWritten || plan.PlannedSkip != r[0].BytesSkipped {
		t.Fatalf("write plan disagrees with result: %+v, %+v", plan, r[0])
	}
	f.disks["00000001"][1][0] ^= 1
	f.corrupt = "00000001"
	if _, err := Flash(context.Background(), f.run, dir, opts); err == nil {
		t.Fatal("optimized flash accepted bad final readback")
	}
}
func TestDifferingRegionsOffsetsAndCoalescing(t *testing.T) {
	dir := t.TempDir()
	a := bytes.Repeat([]byte{0x37}, int(4*compareBlockBytes+512))
	b := append([]byte(nil), a...)
	b[compareBlockBytes] ^= 1
	b[2*compareBlockBytes] ^= 1
	b[4*compareBlockBytes] ^= 1
	src, dst := filepath.Join(dir, "source"), filepath.Join(dir, "target")
	_ = os.WriteFile(src, a, 0600)
	_ = os.WriteFile(dst, b, 0600)
	p := Part{LUN: 0, Start: 100, Sectors: uint64(len(a)) / 512}
	ops, err := differingRegions(context.Background(), src, dst, p, 512)
	if err != nil || len(ops) != 2 || ops[0].Start != 2148 || ops[0].FileOffset != 2048 || ops[0].Count != 4096 || ops[1].Count != 1 || ops[1].FileOffset != 8192 {
		t.Fatalf("%+v %v", ops, err)
	}
	// Exercise offsets against the fake Firehose, not just the descriptors.
	f := &fakeDevice{disks: map[string]map[int][]byte{"00000002": {0: append(make([]byte, 100*512), b...)}}}
	xmlPath, err := writeXML(dir, "restore.xml", ops)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.run(context.Background(), dir, "00000002", "emmc", "loader", xmlPath); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(f.disks["00000002"][0][100*512:], a) {
		t.Fatal("offset writes produced wrong contents")
	}
	if qdl := os.Getenv("GOLDEN_TEST_QDL"); qdl != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, qdl, "--dry-run", "--storage", "emmc", src, xmlPath)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("QDL rejected partial writes: %v\n%s", err, out)
		}
	}
	_ = os.WriteFile(dst, b[:len(b)-1], 0600)
	if _, err := differingRegions(context.Background(), src, dst, p, 512); err == nil {
		t.Fatal("accepted truncated comparison")
	}
}

func TestCompletionNotificationDoesNotWaitForBatch(t *testing.T) {
	dir, f := fixture(t, "unoq", 512)
	f.disks["00000002"] = map[int][]byte{0: diskImage(512, 32), 1: make([]byte, 2048)}
	blocked := make(chan struct{})
	release := make(chan struct{})
	completed := make(chan Result, 2)
	var once sync.Once
	run := func(ctx context.Context, dir, s, storage, loader string, xmls ...string) error {
		if s == "00000003" {
			once.Do(func() { close(blocked) })
			select {
			case <-release:
				return errors.New("simulated target failure")
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return f.run(ctx, dir, s, storage, loader, xmls...)
	}
	opts := options("unoq", "2", "3")
	opts.OnResult = func(r Result) { completed <- r }
	done := make(chan error, 1)
	go func() { _, err := Flash(context.Background(), run, dir, opts); done <- err }()
	select {
	case <-blocked:
	case err := <-done:
		t.Fatalf("batch ended before second target started: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("second target did not start")
	}
	select {
	case r := <-completed:
		if r.Serial != "00000002" || r.Status != "verified" {
			t.Fatal(r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("completion waited for other target")
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("lost target failure")
	}
	r := <-completed
	if r.Serial != "00000003" || r.Status != "failed" {
		t.Fatal(r)
	}
}

func TestSourceRecoveryInBatch(t *testing.T) {
	dir, f := fixture(t, "unoq", 512)
	f.disks["00000002"] = make(map[int][]byte)
	for lun, data := range f.disks["00000001"] {
		f.disks["00000002"][lun] = append([]byte(nil), data...)
	}
	opts := options("unoq", "1", "2")
	opts.RestoreSource = true
	results, err := Flash(context.Background(), f.run, dir, opts)
	if err != nil || len(results) != 2 || results[0].Status != "verified" || results[1].Status != "verified" {
		t.Fatalf("%v %v", results, err)
	}
}
