// SPDX-FileCopyrightText: Arduino s.r.l. and/or its affiliated companies
// SPDX-License-Identifier: GPL-3.0-or-later
package golden

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func largeDigestFixture(t *testing.T) (string, *fakeDevice) {
	t.Helper()
	root := t.TempDir()
	loader := filepath.Join(root, "loader.elf")
	if err := os.WriteFile(loader, []byte("loader"), 0600); err != nil {
		t.Fatal(err)
	}
	disk := diskImage(512, 8192)
	f := &fakeDevice{disks: map[string]map[int][]byte{"00000001": {0: disk}, "00000002": {0: append([]byte(nil), disk...)}}}
	dir := filepath.Join(root, "image")
	if err := Capture(context.Background(), f.run, "unoq", "emmc", "1", loader, dir, []LUN{{Number: 0, SectorSize: 512}}); err != nil {
		t.Fatal(err)
	}
	return dir, f
}

func TestDigestFlashCompleteCoverage(t *testing.T) {
	for _, mode := range []string{"identical", "partial", "coalesced", "full", "collateral"} {
		t.Run(mode, func(t *testing.T) {
			dir, f := largeDigestFixture(t)
			if mode == "partial" || mode == "coalesced" || mode == "collateral" {
				f.disks["00000002"][0][1<<20] ^= 1
			}
			if mode == "coalesced" {
				f.disks["00000002"][0][2<<20] ^= 1
			}
			opts := options("unoq", "2")
			opts.Verification = "digest"
			opts.DigestBlockBytes = 1 << 20
			opts.DigestRun = f.run
			opts.SkipUnchanged = mode != "full"
			write := f.run
			if mode == "collateral" {
				write = func(ctx context.Context, work, s, storage, loader string, paths ...string) error {
					if err := f.run(ctx, work, s, storage, loader, paths...); err != nil {
						return err
					}
					for _, p := range paths {
						if filepath.Base(p) == "restore.xml" {
							f.disks[s][0][3<<20] ^= 1
						}
					}
					return nil
				}
			}
			results, err := Flash(context.Background(), write, dir, opts)
			if mode == "collateral" {
				if err == nil || results[0].Status != "failed" {
					t.Fatal("collateral corruption passed")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			r := results[0]
			want := uint64(0)
			if mode == "partial" {
				want = 1 << 20
			}
			if mode == "coalesced" {
				want = 2 << 20
			}
			if mode == "full" {
				want = 4 << 20
			}
			if r.Status != "verified" || r.Verification != "digest" || r.BytesWritten != want || r.BytesSkipped != 4<<20-want {
				t.Fatal(r)
			}
			if want > 0 && f.writes != 1 {
				t.Fatalf("expected one coalesced write, got %d", f.writes)
			}
			if !bytes.Equal(f.disks["00000001"][0], f.disks["00000002"][0]) {
				t.Fatal("target differs")
			}
		})
	}
}

func TestDigestRejectsResponsesBeforeWriting(t *testing.T) {
	for _, value := range []string{"missing", "short", "invalid", "long", "nak", "cancelled"} {
		t.Run(value, func(t *testing.T) {
			dir, f := largeDigestFixture(t)
			opts := options("unoq", "2")
			opts.Verification = "digest"
			opts.DigestRun = func(ctx context.Context, work, s, storage, loader string, paths ...string) error {
				if value == "nak" {
					return errors.New("NAK")
				}
				if value == "cancelled" {
					return context.Canceled
				}
				data, err := os.ReadFile(paths[0])
				if err != nil {
					return err
				}
				var doc struct {
					Ops []operation `xml:",any"`
				}
				if err = xml.Unmarshal(data, &doc); err != nil {
					return err
				}
				for _, op := range doc.Ops {
					if value == "missing" {
						continue
					}
					text := strings.Repeat("a", 64)
					switch value {
					case "short":
						text = text[:63]
					case "invalid":
						text = "z" + text[1:]
					case "long":
						text += "a"
					}
					if err = os.WriteFile(filepath.Join(work, op.File), []byte(text), 0600); err != nil {
						return err
					}
				}
				return nil
			}
			r, err := Flash(context.Background(), f.run, dir, opts)
			if err == nil || r[0].Status != "failed" || f.writes != 0 {
				t.Fatalf("accepted %s: %v %v writes=%d", value, r, err, f.writes)
			}
		})
	}
}

func TestDigestConcurrentMultipleLUNs(t *testing.T) {
	for _, v := range []struct {
		board  string
		sector uint64
	}{{"unoq", 512}, {"ventunoq", 4096}} {
		t.Run(v.board, func(t *testing.T) {
			dir, f := fixture(t, v.board, v.sector)
			for _, s := range []string{"00000002", "00000003"} {
				f.disks[s] = map[int][]byte{0: append([]byte(nil), f.disks["00000001"][0]...), 1: make([]byte, len(f.disks["00000001"][1]))}
			}
			opts := options(v.board, "2", "3")
			opts.SkipUnchanged = true
			opts.Verification = "digest"
			opts.DigestRun = f.run
			r, err := Flash(context.Background(), f.run, dir, opts)
			if err != nil {
				t.Fatal(err)
			}
			for _, result := range r {
				for lun, want := range f.disks["00000001"] {
					if !bytes.Equal(want, f.disks[result.Serial][lun]) {
						t.Fatal("LUN differs")
					}
				}
			}
		})
	}
}
