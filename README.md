# Arduino Flasher CLI

A tool to download and flash Debian images on the board.

## Docs

For a full guide on how to use it, see the [User documentation](https://docs.arduino.cc/tutorials/uno-q/update-image/).

## Build and test it locally

Build it with `task build` and run:

```sh
# Flash the most recent Debian image for the UNO Q
./build/arduino-flasher-cli flash unoq

# Flash a local image. It works with either an archived or extracted image
./build/arduino-flasher-cli flash path/to/downloaded/image
```

## Golden images and batch flashing

Capture a configured UNO Q or VENTUNO Q, then copy its Linux storage to other
boards of the same model and storage capacity. Shut down the source cleanly,
connect it in EDL mode, and leave the EDL jumper fitted during capture:

```sh
arduino-flasher-cli golden capture ./golden-unoq --board unoq
```

With exactly one EDL device connected, its serial is selected automatically.
With several connected, add `--serial 0x1234ABCD` to choose the source. The output
directory must not already exist. For VENTUNO Q, use `--board ventunoq` and an
output name such as `./golden-ventunoq`.

The matching Firehose programmer is obtained automatically from the latest
official image for the board's default distribution. The image archive is
checksum-verified and retained in the shared cache; only the programmer is
extracted. The first run may download several gigabytes. Use `--version VERSION`
to select a particular image release, or `--programmer /path/to/prog_firehose_ddr.elf`
to use a local programmer without accessing the image index.

Connect the matching target boards in EDL mode, then preview and run a batch:

```sh
# List connected EDL devices and their serials
arduino-flasher-cli golden devices

# Check the image and preview targets without writing
arduino-flasher-cli golden flash ./golden-unoq --board unoq --all --dry-run

# Flash up to 16 boards concurrently and verify the complete contents
arduino-flasher-cli golden flash ./golden-unoq --board unoq --all --yes
```

`--all` selects connected EDL devices and excludes the captured source. It cannot
identify board models, so connect only matching targets. To select specific
boards, replace `--all` with `--serial 0x11111111,0x22222222`.
Each board prints `VERIFIED` with its serial when finished; remove that board's
EDL jumper and power-cycle it to boot Linux.

Capture and flashing show compact progress with bytes, elapsed time, speed and
an approximate phase ETA. Updates identify each board and appear at progress
milestones, with a one-minute heartbeat during waits. Write progress counts
acknowledged chunks. A final table compares write, readback, checksum and total
times and written/skipped bytes for each target. With `--skip-unchanged`, the
comparison reports how much needs writing and identifies boards already matching
the image. Add `--verbose-progress` for frequent diagnostic updates.
Progress goes to stderr; JSON results stay on stdout.

Concurrency defaults to **16 boards**; `--jobs` accepts 1–32. Capture needs space
for the full raw storage. Default readback verification needs one image's worth
of scratch space per active worker (about 251 GB for 16 UNO Q targets). Digest
verification uses only small target scratch files.

### Fastest batch settings: storage digests

Digest mode hashes storage on each board and transfers only SHA-256 responses
for comparison and verification. It still verifies every captured sector. Two
already-matching UNO Q boards verified concurrently in **2m23s total**, including
source checks, with no writes needed. This is verification timing, not factory
flashing time; 16-board throughput has not yet been measured.

Build the helper once on macOS or Linux. Install a C compiler, `make`, `patch`,
`curl`, Python 3, `pkg-config`, libusb and libxml2. On macOS:

```sh
brew install pkg-config libusb libxml2
scripts/qdl-digest/build.sh
```

From the repository root, use the rebuilt CLI and helper for similar images or
repeat provisioning:

```sh
./build/arduino-flasher-cli golden flash ./golden-unoq \
  --board unoq --all --jobs 16 --skip-unchanged \
  --verification digest --digest-qdl ./build/qdl-digest \
  --digest-block-mib 64 --yes
```

This skips matching ranges, locates changes in 64 MiB blocks, writes differences,
and verifies the complete image again after any writes. Source block hashes are
prepared once for the whole batch. An identical board needs no second pass.
`--jobs 16` and `--digest-block-mib 64` are shown explicitly; both are defaults.

For partial writes when differences are spread across most image chunks, add
`--digest-comparison blocks`. This compares each 64 MiB block directly in one
pass, avoiding the initial 1 GiB comparison followed by refinement. It still
verifies all selected storage after any writes. The default `hierarchical`
strategy uses fewer digest requests on identical or lightly changed boards.
`blocks` requires both `--skip-unchanged` and `--verification digest`.

For factory images that differ across most of storage, try a full write with
digest verification to avoid the comparison passes:

```sh
./build/arduino-flasher-cli golden flash ./golden-unoq \
  --board unoq --all --jobs 16 \
  --verification digest --digest-qdl ./build/qdl-digest --yes
```

There is no single fastest setting for every factory release. Compare these two
modes on representative boards. `--digest-block-mib 1024` reduces refinement
command count but can write an entire 1 GiB range for a small difference; smaller
blocks save writes at the cost of more hash commands.

For a single VENTUNO Q, the tested **full write with digest verification** took
**36m47s**, versus **45m46s** for the preceding factory-board skip-mode restore
(8m59s less, about 20%). Skip mode spent 14m08s comparing/refining to avoid only
8.72 GiB of writes. The full repeat wrote all 59.31 GiB to the same already-restored
target, so these are observations on different starting contents, not a controlled
factory comparison. This result does not establish the fastest multi-board mode.

```sh
./build/arduino-flasher-cli golden flash ./golden-ventunoq \
  --board ventunoq --all --jobs 16 --timeout 3h \
  --verification digest --digest-qdl ./build/qdl-digest --yes
```

The command above runs a full-write batch for comparison.

For many factory VENTUNO Q boards sharing a USB 2 hub uplink, benchmark **single-pass
block comparison** first. It saves transfer bandwidth while hashing on each target:

```sh
./build/arduino-flasher-cli golden flash ./golden-ventunoq \
  --board ventunoq --all --jobs 16 --timeout 3h --skip-unchanged \
  --verification digest --digest-qdl ./build/qdl-digest \
  --digest-comparison blocks --digest-block-mib 64 --yes
```

If 16 boards have the same matching ranges as the tested factory target, full
writes transfer about **949 GiB**, while partial writes transfer **809.5 GiB**,
saving **139.5 GiB**. At a hypothetical shared aggregate rate of 32 MiB/s, that
avoids about **74 minutes of writes**; this is arithmetic, not a 16-board benchmark.
Target comparison work can overlap across workers, so its cost does not simply
multiply by 16. On independent USB uplinks or targets with almost no matching
regions, full-write mode may still win. Measure both modes on the intended batch
topology; single-pass factory partial writes and 16-board throughput remain untested.

For the highest write throughput, distribute boards across independent USB
controllers. Extra hubs sharing one controller may still share bandwidth. Lower
`--jobs` if transfers slow down or become unreliable; more workers cannot remove
a shared USB bandwidth limit. Increasing concurrency above 16 only helps when
more than 16 targets are connected.

Digest mode requires a compatible programmer. UNO Q and VENTUNO Q digest flashing
have been hardware-tested, including complete final storage verification.
Use `--verification readback` without the helper as the fallback. Missing digests, command failures and
mismatches fail verification.

Golden images include Linux settings, credentials and device identities, but
**do not capture or flash the separate MCU firmware**. See the
[golden-image guide](docs/golden-images.md) for preparation, storage requirements,
advanced options and the [UNO Q validation results](docs/unoq-validation.md).

See the [digest-mode guide](docs/golden-images.md#storage-digest-verification)
for helper details, verification behavior and platform support.

### Hardware test results

Recorded on **4–5 November 2026**, using macOS arm64. UNO Q images cover
15,675,162,624 bytes (14.60 GiB) per board; the tested VENTUNO Q covers
63,685,787,648 bytes (59.31 GiB) across LUNs 0 and 1. All completed capture/flash tests
below passed; timing varies with target contents and the USB connection.
Three-board batches are user-reported transcripts; the other measurements were
run locally. Different boards/images were used between batches, so these are
observations rather than controlled speed comparisons.

| Test | Boards | Whole capture/batch time | Outcome |
| --- | ---: | ---: | --- |
| VENTUNO Q complete capture (`80796883`, USB 2) | 1 | **28m53s** | 59.31 GiB saved; all bundle checksums and both GPTs validated |
| VENTUNO Q box-fresh partial restore, digest mode (`868C69F1`, USB 2, 5 November) | 1 | **45m46s** | Verified all 59.31 GiB; wrote 50.59 GiB, skipped 8.72 GiB (14.7%); boot not checked |
| VENTUNO Q full-write repeat, digest mode (`868C69F1`, same USB 2 path) | 1 | **36m47s** | All 59.31 GiB written and verified; 8m59s less than preceding skip-mode restore; target already restored |
| VENTUNO Q single-pass block comparison, matching target (`868C69F1`, USB 2) | 1 | **7m18s** | All 59.31 GiB matched in one 64 MiB block comparison pass (6m28s); zero writes, no refinement or second hash pass |
| VENTUNO Q direct USB cable, read-only transport check (`868C69F1`) | 1 | **4.001s** including startup | 1 MiB digest and 64 MiB read matched; no hubs in path, but still USB 2 before/after Firehose; no USB 3 speedup demonstrated |
| VENTUNO Q digest restore attempt (`868C69F1`, 5 November) | 1 | Probe stopped after 58.4s | Target disappeared before QDL opened it; source checksums and block digest preparation passed (24s each); no storage writes or target verification |
| UNO Q complete capture | 1 | 2m44s | Full image saved with checksums; repeated capture matched |
| UNO Q full restore + complete readback | 1 | 16m31s | Every captured sector matched; 14.60 GiB written |
| UNO Q capture + verified restore | 1 | 19m15s | End-to-end storage round trip passed |
| UNO Q partial restore after Linux boot, readback mode | 1 | 4m43s | 168 MiB written; 98.88% skipped; full final readback passed |
| UNO Q full writes on one hub, readback mode | 3 | 36m08s | Three verified; each wrote 14.60 GiB |
| UNO Q partial writes on one hub, readback mode | 3 | 34m29s | Three verified; combined writes 7.58 GiB, 82.68% less than three full writes |
| UNO Q complete storage digests, research helper | 2 | About 2m06s concurrently | All 15 manifest ranges matched on both boards; no writes |
| UNO Q identical targets, integrated digest mode | 2 | 2m23s | Both verified in about 2m12s each; zero writes |
| UNO Q `golden-unoq-llm`, partial writes + digest verification | 2 | **16m09s** | Both verified; each wrote 10.85 GiB and skipped 3.75 GiB (25.7%) |

Per-target results for the concurrent flashing batches:

| Batch / serial | Initial compare | Refine / local compare | Write | Final verification | Worker total | Written / skipped |
| --- | ---: | ---: | ---: | ---: | ---: | --- |
| Full readback / `8ED963AB` | — | — | 15m07s | 15m52s readback + 6s checksums | 31m08s | 14.60 GiB / 0 B |
| Full readback / `C2B2F977` | — | — | 19m56s | 15m49s readback + 6s checksums | 35m53s | 14.60 GiB / 0 B |
| Full readback / `4A712118` | — | — | 20m30s | 15m25s readback + 6s checksums | 36m03s | 14.60 GiB / 0 B |
| Partial readback / `C2B2F977` | 17m47s pre-read | 3s | — | Verified by pre-read | 17m52s | 0 B / 14.60 GiB |
| Partial readback / `8ED963AB` | 17m48s pre-read | 3s | 13s | 11m00s readback + 6s checksums | 29m12s | 102 MiB / 99.32% |
| Partial readback / `7FE12D45` | 17m59s pre-read | 3s | 7m41s | 8m33s readback + 6s checksums | 34m23s | 7.48 GiB / 48.73% |
| Identical digest / `8ED963AB` | 2m09s | — | — | Verified by initial digests | 2m12s | 0 B / 14.60 GiB |
| Identical digest / `C2B2F977` | 2m09s | — | — | Verified by initial digests | 2m12s | 0 B / 14.60 GiB |
| LLM digest / `8ED963AB` | 2m10s | 2m23s | 9m13s | 2m09s complete digests | 15m57s | 10.85 GiB / 3.75 GiB |
| LLM digest / `C2B2F977` | 2m09s | 2m22s | 9m00s | 2m09s complete digests | 15m43s | 10.85 GiB / 3.75 GiB |

Worker totals include probing and setup; batch totals also include shared source
validation and block-hash preparation. Phase timings are rounded and may not sum
exactly. Initial comparison verification applies only to targets needing no writes;
all written targets received complete final verification.

Additional validation passed:

- UNO Q normal boot after the initial full restore: **30.403s**, Debian 13.2,
  no failed systemd units, root and `/home/arduino` mounted read/write.
- Primary/backup GPT CRCs and partition bounds; repeated capture equality.
- Target storage SHA-256 matched source for 1 MiB, 64 MiB and 1 GiB ranges.
  The combined 64 MiB + 1 GiB research invocation took **10.67s**.
- Two same-byte 1 MiB hardware writes followed by matching storage digests,
  including a nonzero source-file offset. Target contents were preserved.
- VENTUNO Q complete bundle checksums, both LUNs’ primary/backup GPT CRCs and
  partition bounds, and read-only digest samples passed. Phase timings and USB
  transport checks are recorded in the [VENTUNO Q validation report](docs/ventunoq-validation.md).
- Full Go race suite and `go vet`; byte-addressed device tests cover full/partial
  writes, coalescing, multiple LUNs, both sector sizes, concurrency, cancellation,
  malformed/missing digests and corruption outside the written ranges.
- Native QDL digest parser tests cover valid hashes, ACK/NAK, duplicates and
  malformed/missing responses. Compact logger replay reduced 1,255 synthetic
  progress events to 180 lines (86% fewer).

Post-flash boot was only checked for the initial single-board full restore.
These tests cover AP storage, not MCU firmware. Sixteen-board throughput remains
untested. VENTUNO Q capture and digest restore passed; post-restore boot has not
been tested.
Detailed records: [single-board tests](docs/unoq-validation.md),
[three-board batches](docs/unoq-three-board-validation.md),
[digest research and validation](docs/golden-speed-research.md), and
[VENTUNO Q capture and restore validation](docs/ventunoq-validation.md).
Local raw logs and JSON are retained under `build/`, including
`golden-unoq-llm-flash-progress.log`, `golden-unoq-llm-flash-results.json`,
`ventunoq-capture-progress.log`, `ventunoq-validation/results.json`,
`ventunoq-restore-progress.log`, `ventunoq-restore-results.json`,
`ventunoq-full-digest-progress.log`, `ventunoq-full-digest-results.json`,
`ventunoq-block-comparison-progress.log` and `ventunoq-block-comparison-results.json`;
these artifacts and captured images are excluded from commits.

## Download images

`arduino-flasher-cli download unoq` saves the image archive in the user cache
under `arduino-flasher-cli/images/`. Each invocation reads the online manifest
and reuses a cached archive only if its SHA-256 checksum matches the release.

Use `--dest-dir` to download to an existing directory instead of the cache:

```sh
arduino-flasher-cli download unoq --dest-dir .
```

## Reuse images when flashing

`arduino-flasher-cli flash unoq` checks the online manifest and reuses a cached
archive when its checksum matches. If no valid archive is cached, the image is
downloaded temporarily and removed after the operation, including on failure.

Use `--cache` to retain newly downloaded archives for future flashes:

```sh
arduino-flasher-cli flash unoq --cache
```

Interactive image selections mark matching local archives with `(cached)`. The
flash wizard also asks whether to keep new downloads. Existing cached archives
are retained regardless of this choice, including when flashing fails.
Extraction always uses a temporary directory. `--temp-dir` selects its parent;
cached archives stay in the user cache. Local image files and directories are
flashed directly without being added to the cache.
When reusing a verified cached archive, the free-space check only accounts for
extraction.

## Manage cached images

Inspect or clear the shared image cache with:

```sh
arduino-flasher-cli cache list
arduino-flasher-cli cache list --format json
arduino-flasher-cli cache clear
```

`cache list` works offline and reports each complete image's board, distribution,
version, archive size, download date and path (in JSON output). Total cache size
includes metadata and incomplete downloads. Listing does not recheck archive
checksums; those are verified against the online manifest before reuse.

`cache clear` immediately removes images and incomplete downloads from the
`images/` cache directory. Empty synchronization files in `.locks/` are preserved
so concurrent processes continue to share the same publication locks.
Files downloaded to another directory with `--dest-dir` and temporary directories
outside `images/` are preserved. Verified archives and metadata are published
together by renaming their complete temporary directory.
Both commands also work when the cache has not been created yet.

The normal `list` command marks downloaded images in the `CACHED` column and
includes a `cached` boolean in JSON output. It compares local metadata with the
online image's board, distribution, version, checksum and archive name, without
reading archive contents. A matching image with same-size corruption can still
appear cached; its checksum is verified before reuse by `download` or `flash`.
If the cache cannot be inspected, `list` reports a warning and still lists the
online images without cache marks.

## Cache behavior over gRPC

The `Flash` RPC uses the same image cache as the CLI. It reads the online manifest
and verifies cached archives before reuse. Set `FlashRequest.cache` to `true` to
retain new downloads; the default is `false`, matching `flash` without `--cache`.
Cached archives survive both successful and failed flashes.

`FlashRequest.temp_path` selects the parent of a private per-request directory.
An empty path uses the user cache directory. This private directory is removed
on success, failure or cancellation; existing files in the parent are preserved.
This replaces the previous behavior of retaining downloads there after a failed
flash. Clients that need persistent images should opt in with `cache`.

Download start/end events are also sent on a cache hit, with a completed progress
update and a `Using cached image` end message. Extraction and flash progress use
the existing response messages.

## Read an additional index

Only the published indexes are read by default. `ARDUINO_FLASHER_ADDITIONAL_URLS`
adds more, separated by commas or newlines, and they are read _alongside_ the
published ones rather than instead of them:

```sh
ARDUINO_FLASHER_ADDITIONAL_URLS="http://127.0.0.1:3001/debian-im/Stable/info.json" \
  ./build/arduino-flasher-cli list
```

`ARDUINO_FLASHER_ADDITIONAL_HEADERS` adds HTTP headers, one `Name: value` per
line, sent both when reading an index and when downloading the image it points
at, for a host that asks to be authenticated:

```sh
ARDUINO_FLASHER_ADDITIONAL_HEADERS="Authorization: Bearer $TOKEN" \
  ./build/arduino-flasher-cli list
```

An index that cannot be read is reported as a warning and the others are still
listed. One that is not published yet is passed over in silence.

## Update QDL version

flasher-cli embeds a statically builded [qdl](https://github.com/linux-msm/qdl) binary taken from the https://github.com/arduino/qdl-packing repo. To update the qdl version you need to:

1. Make sure the qdl-packing repo has the desired version of qdl (check the release page [here](https://github.com/arduino/qdl-packing/releases)) if not, create a PR to update it.

2. Update the `TAG` variable in script `internal/artifacts/download_resources.sh` to the desired version.

3. Test that everything works by running `task clean build` and testing the resulting binary.

4. Create a new release of flasher-cli, the build process will automatically download and embed the new qdl binary.
