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

For the highest write throughput, distribute boards across independent USB
controllers. Extra hubs sharing one controller may still share bandwidth. Lower
`--jobs` if transfers slow down or become unreliable; more workers cannot remove
a shared USB bandwidth limit. Increasing concurrency above 16 only helps when
more than 16 targets are connected.

Digest mode requires a compatible programmer; UNO Q has been hardware-tested,
while VENTUNO Q digest support still needs validation. Use `--verification readback` without the helper as the fallback. Missing digests, command failures
and mismatches fail verification.

Golden images include Linux settings, credentials and device identities, but
**do not capture or flash the separate MCU firmware**. See the
[golden-image guide](docs/golden-images.md) for preparation, storage requirements,
advanced options and the [UNO Q validation results](docs/unoq-validation.md).

See the [digest-mode guide](docs/golden-images.md#storage-digest-verification)
for helper details, verification behavior and platform support.

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
