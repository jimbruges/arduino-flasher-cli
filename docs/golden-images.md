<!--
SPDX-FileCopyrightText: Arduino s.r.l. and/or its affiliated companies
SPDX-License-Identifier: GPL-3.0-or-later
-->

# Golden image capture and concurrent restore

`golden capture` reads a powered-down Linux installation through Qualcomm EDL / Firehose.
`golden flash` restores that bundle to explicitly selected serials concurrently and
reads every written byte back to verify its SHA-256 digest. Both `unoq` and
`ventunoq` board identifiers are supported. The transport supports eMMC and UFS,
512-byte and 4096-byte sectors, multiple GPT LUNs, and explicitly sized raw boot LUNs.

A physical UNO Q capture of 15,675,162,624 bytes has completed successfully.
A restore to that same UNO Q followed by full readback and normal Linux boot
also passed.
A subsequent user-reported three-board UNO Q batch also passed full verification;
see [the batch comparison](unoq-three-board-validation.md).
Software tests exercise both geometries and the real bundled QDL parser in dry-run
mode; see the validation record below for the tested scope.

## Prepare the source and targets

1. Customize the source board, then shut Linux down cleanly. Do not capture a live
   root filesystem using `dd` over ADB.
2. Select the correct `--board`. Capture automatically obtains its **official
   image's Firehose programmer**, or you can supply a matching local programmer
   with `--programmer`. Do not use an UNO Q programmer on a VENTUNO Q.
3. Identify storage type, logical sector size, and **every physical LUN** required
   by the official image's rawprogram XMLs. `physical_partition_number` is a LUN,
   not a Linux GPT partition number. A selected GPT LUN includes every partition,
   unallocated space, and both GPT copies. Boot LUNs without GPT need their full
   documented sector count through `--raw-lun NUMBER:SECTORS`.
4. Put the source in EDL using the board's documented procedure. Keep the EDL
   strap/jumper asserted during the operation: bundled QDL resets between the
   geometry probe and transfer. A board that does not re-enter EDL must be put
   back into EDL manually before the timeout. Use stable power and USB cables.
5. Obtain the EDL serials. They are **not necessarily ADB serials**. Numeric CLI
   serials are decimal; prefix hexadecimal values with `0x`, including digit-only
   hexadecimal serials. The command prints canonical hexadecimal serials in its
   logs/results. Windows needs the same QDL USB drivers as the existing flasher
   (`arduino-flasher-cli install-drivers`); Linux needs appropriate USB permissions.

## Capture

For an UNO Q whose official layout uses eMMC, 512-byte sectors and GPT LUN 0:

```sh
arduino-flasher-cli golden capture ./golden-unoq --board unoq
```

Omit `--serial` to automatically select the sole connected Qualcomm EDL device.
No connected device or multiple devices produces an error before capture starts;
with multiple devices, select one using `--serial 0x1234ABCD`. The selected serial
is printed before capture. Discovery does not identify the board model, so
`--board` is still required.

Add all required non-GPT boot LUNs with `--raw-lun NUMBER:SECTORS`, using the
physical sizes from the board's storage documentation. Do not infer a raw LUN's
capacity from a single bootloader payload's size. Do not list a raw LUN in `--luns`.

The official VENTUNO Q image `20260729-248` uses **eMMC, 512-byte sectors,
and GPT LUNs 0 and 1**. Its LUN 1 contains board configuration data and must not
be omitted from a complete capture:

```sh
arduino-flasher-cli golden capture ./golden-ventunoq --board ventunoq
```

Defaults are `--storage emmc --sector-size 512 --luns auto`. Auto selects LUN 0
for UNO Q and LUNs 0,1 for VENTUNO Q, matching the inspected official releases.
Override these flags only when your hardware's official layout differs. Use the
programmer from the image's `flash` directory, not a programmer from another
board or a component subdirectory.

When `--programmer` is omitted, capture resolves the latest official image for
that board's default distribution (Debian for UNO Q, Ubuntu for VENTUNO Q).
It downloads or reuses the shared checksum-verified image cache, then reads the
`.tar.zst` archive and extracts only `flash/prog_firehose_ddr.elf` (or the legacy
`flash_UnoQ` layout). Component programmers are excluded; missing or ambiguous
programmers fail before any capture transfer. The archive stays cached, and the
temporary programmer is removed after being copied into the golden bundle.
The first run can download several gigabytes. Online index access is required
even for cached images. `--version VERSION` pins a release and `--os OS` overrides
the default distribution; these flags cannot be combined with `--programmer`.
The capture timeout and reported duration include automatic programmer setup.

For offline use, supply `--programmer /path/to/prog_firehose_ddr.elf` explicitly.
An invalid explicit path produces an error instead of silently downloading a
replacement. To obtain that file manually, download an official image using the
existing CLI and extract its archive before selecting the programmer:

```sh
arduino-flasher-cli download unoq --dest-dir .
arduino-flasher-cli download ventunoq --dest-dir .
# Extract the downloaded .tar.zst with an archive tool supporting Zstandard.
```

The output directory must not already exist. It contains a programmer, raw files
of at most 1 GiB each, and a versioned `manifest.json` with geometry, source serial,
and SHA-256 hashes. The manifest appears only after capture succeeds. Failed
captures remove their partial output. Capture needs free space equal to the full
selected storage size; it is not compressed or sparse. Default timeout is two hours.

## Inspect and restore a batch

```sh
# Validate every bundle checksum and target selection without USB access.
arduino-flasher-cli golden flash ./golden-unoq \
  --board unoq --serial 0x11111111,0x22222222,0x33333333 --dry-run

# Restore up to two targets concurrently; readback verification is mandatory.
arduino-flasher-cli golden flash ./golden-unoq \
  --board unoq --serial 0x11111111,0x22222222,0x33333333 \
  --jobs 2 --temp-dir /path/to/large/scratch --timeout 2h --yes
```

`--serial` may also be repeated. To discover targets instead, use:

```sh
# Read-only USB inventory; serials include 0x for unambiguous reuse.
arduino-flasher-cli golden devices

# Verify the bundle and preview the currently connected target serials.
arduino-flasher-cli golden flash ./golden-unoq --board unoq --all --dry-run

# Flash that invocation's connected targets, up to four at a time.
arduino-flasher-cli golden flash ./golden-unoq --board unoq --all --jobs 4 --yes
```

`--all` snapshots connected Qualcomm EDL (`05c6:9008`) devices once, excludes the
source serial, and uses an explicit serial for every subsequent QDL invocation.
New arrivals are not added to an ongoing batch. Preview and flash are separate
snapshots, so avoid changing connections between them. No remaining targets is
an error; there is no fallback to the source. Duplicate serials on different USB
paths, missing serial descriptors, and inventory errors stop discovery.

Discovery reads Linux USB sysfs, macOS IORegistry, or Windows PnP bus-reported USB
product descriptors. It does not claim interfaces, reboot normal-mode boards, or
infer serials from COM port numbers. The bundled QDL requires the serial exposed
in the Qualcomm USB product descriptor. Normal Linux/ADB devices and Qualcomm
crash-dump mode are excluded.

**EDL discovery does not identify the Arduino model.** Connect only matching,
already provisioned board/storage variants for an `--all` batch. `--board` must
match the manifest and remains an operator declaration. Other Qualcomm EDL
hardware can appear in the inventory. Prefer explicit serials when unrelated
EDL devices share the host. `--all` cannot be combined with `--serial` or
`--restore-source`; ordinary explicit selections still reject duplicate serials
and the source.

For a deliberate single-board recovery or round-trip test, `--restore-source`
allows exactly one target and requires that it match the captured source serial.
`--yes` is still required to write; ordinary batch flashing still rejects the source:

```sh
arduino-flasher-cli golden flash ./golden-unoq \
  --board unoq --serial 0x1234ABCD --restore-source --jobs 1 --yes
```

Capture JSON includes `duration_seconds` for the capture operation. Each flash
result includes its worker's duration (preflight, write/readback, and checksum
comparison); initial shared image validation is outside that per-worker value.
QDL diagnostics also report the duration of each invocation. Capture and flashing
rates include raw unallocated storage; they are not compressed-payload rates.

Progress is enabled automatically and goes to stderr, keeping stdout available
for JSON results. Compact output shows phase starts/completions, progress after
at least five percentage points or 30 seconds of advancing data (at most once
per five seconds), and a one-minute heartbeat when no new progress is reported.
Quick probes, queued targets and final checksum progress are suppressed unless
there is a failure or a prolonged wait. Each board still prints `VERIFIED` or
`FAILED` immediately; the final table shows write, readback, checksum and total
times, plus pre-read/comparison times when those phases run. Missing phases use
`—`; a verified unchanged target may require no writes or second readback.
Use `--verbose-progress` on capture or flash for all phases and frequent updates.

Read progress measures bytes received into local files. Write progress counts
only chunks acknowledged by QDL, so its percentage can pause during a large
chunk (up to 1 GiB). Write rates use the elapsed time at the last acknowledgement;
unchanged heartbeats identify the awaited chunk and omit rate/ETA. This avoids
artificially degrading the estimate between measurements. ETAs apply to the
current phase, not the entire workflow or batch, and change with USB contention.
A phase reaching 100% does not mean a board is ready: only the final `VERIFIED`
result confirms successful full verification. Failures retain measured counts.
Capture and batch completion show total elapsed time, including shared image
validation or programmer setup.

Each worker probes every selected LUN before writing. GPT LUNs must have exactly
matching sector counts, determined from a CRC-checked GPT header's backup-header
location. Targets with blank or damaged GPT must first receive an official image.
A raw LUN's probe checks readability at the end of its declared extent; it cannot
prove the full physical capacity or detect a larger raw LUN. Use matching hardware
and documented raw geometry. Growing/shrinking images and UFS provisioning are
not performed.

Readback uses a private temporary directory per active worker and needs one full
image worth of free space per worker, **in addition to the golden bundle**. The
command checks this before starting workers. For a 32 GB image and `--jobs 4`,
allow about 128 GB of scratch space. Competing processes can consume that space
after the check. Lower `--jobs` for limited disk space or USB bandwidth.

Standard output contains JSON results (`planned`, `verified`, or `failed`) in
input serial order; diagnostics go to stderr. A failed target does not prevent
other targets from finishing. Any failure produces a nonzero process exit code.
Ctrl-C terminates running QDL processes and cancels queued jobs. Failed/interrupted
writes may leave a target unbootable; re-enter EDL and retry that target after
fixing the cause. Temporary readback data is removed on completion or failure.
After success, remove the EDL strap and power-cycle the targets.

Each target also prints an immediate `[0xSERIAL] VERIFIED` message on stderr
after its verification and scratch cleanup, without waiting for the other workers.
This identifies which board can be disconnected while the batch continues.
Failed targets print `FAILED` and must not be treated as ready.

## Reduce image size and avoid unnecessary writes

A golden bundle holds the entire raw device, including unused sectors. The
official download is compressed; comparing its archive size with the extracted
golden directory overstates the difference. The measured UNO Q golden bundle
compressed from 14.60 GiB to 2.43 GiB with Zstandard level 3, versus about 2.1 GiB
for the official download. Compression reduces storage/distribution size, not
the number of bytes written by QDL. The CLI currently flashes extracted bundles.

To compress a bundle for distribution and extract it later:

```sh
umask 077
tar -cf - -C golden-unoq . | zstd -T0 -3 -o golden-unoq.tar.zst
mkdir restored-bundle
zstd -dc golden-unoq.tar.zst | tar -xf - -C restored-bundle
```

For targets with similar existing contents, enable read-before-write comparison:

```sh
arduino-flasher-cli golden flash ./golden-unoq \
  --board unoq --all --jobs 4 --skip-unchanged --yes
```

This reads each target in full, compares 1 MiB regions against the verified source,
and writes only differing regions, coalescing adjacent changes. It then reads and
verifies the **entire** result. If the pre-read already matches every byte, no
writes are issued and that comparison is the verification. JSON results report
`bytes_written` and `bytes_skipped`; `--dry-run` does not access the target or
predict savings.

After comparison, diagnostics report planned written/skipped bytes. An identical
target is explicitly reported as verified by the pre-read without writing.
The final timing table also includes written/skipped byte counts; unknown partial
write counts on failed transfers use `—` instead of implying zero writes.

This preserves the exact image, including unused space: zero-filled source blocks
are only skipped when the corresponding target bytes actually match. It does not
assume that factory targets are blank or that an erase command returns zeroes.
The extra pre-read can make flashing slower when nearly every region differs.
The mode is opt-in; savings depend on target contents. On the tested UNO Q,
restoring after a previous restore and Linux boot took **4m 43s**, versus
**16m 31s** for a full restore (3.50× faster). It wrote 168 MiB and skipped
98.88% of storage; complete final readback passed. This closely matching target
does not predict the savings on factory boards or unrelated installations.

In the [user-reported three-board hub batch](unoq-three-board-validation.md),
this mode reduced writes by 82.68% but reduced batch wall time only from 36m 08s
to 34m 29s. The full pre-read took nearly 18 minutes; two targets then needed
full final readback. One target was replaced between runs, so this is an
observational comparison. An identical target needed no writes and finished
after its pre-read in 17m 52s. Write savings do not directly predict time savings
when the shared USB path limits reading.

## Storage digest verification

For multi-board fixtures, verify storage on each board and transfer only SHA-256
responses over USB. Build the native helper once (macOS/Linux; see its
[build requirements](../scripts/qdl-digest/README.md)):

```sh
scripts/qdl-digest/build.sh
./build/arduino-flasher-cli golden flash ./golden-unoq \
  --board unoq --all --jobs 4 --skip-unchanged \
  --verification digest --digest-qdl ./build/qdl-digest --yes
```

The CLI validates the complete source bundle as usual. With `--skip-unchanged`,
it prepares smaller source block hashes once per batch, then asks each board to
hash the manifest's ranges (up to 1 GiB). Matching ranges are verified and skipped.
Mismatching ranges are compared in 64 MiB blocks; adjacent changed blocks within
a source file are coalesced and written. Any written target is then hashed across
**every captured sector**, including unchanged and unused regions. An identical
target needs no writes or second hash pass.

`--digest-block-mib` selects a power of two from 1 to 1024 (default 64). Smaller
blocks can reduce unnecessary writes but require more target commands. This
comparison granularity differs from readback mode's 1 MiB byte comparison.
Hashing a mismatched range twice adds work when most contents differ; benchmark
against digest mode without `--skip-unchanged` for substantially older images.
Without `--skip-unchanged`, digest mode checks command support, writes the full
image, and verifies the complete image with storage digests.

Progress and the final table show digest comparison, refinement and verification
times. JSON includes `verification` (`digest` or `readback`) alongside written and
skipped byte counts. Digest mode needs only small per-worker scratch files instead
of a full image of target readback data. It still reads source files locally and
writes raw data over USB; it cannot remove a hub's write-bandwidth limit.

Missing/malformed digests, NAKs, timeouts and mismatches fail the target. Digest
mode requires a compatible programmer and helper, and does not silently weaken
verification or change modes after an error. Use `--verification readback` (the
default), without `--digest-qdl`, for programmers/platforms without digest support.
Storage digests trust the same supplied programmer used for reading/writing.
UNO Q hardware support has been tested; VENTUNO Q digest support still needs a
hardware trial, although both sector sizes and multiple LUNs are covered in tests.

## What is cloned

This is a byte-for-byte copy of **the selected application-processor storage
LUNs**, including personal data, passwords, SSH host keys, machine IDs, partition
UUIDs and device-specific calibration data stored there. Prepare an appropriate
first-boot identity regeneration process for a fleet, and verify that copied
calibration/security material is portable on your hardware. Bundles are created
with owner-only access; store and distribute them accordingly. Do not modify a
bundle while it is being flashed or run two flasher processes against the same
serial.

This does not read the separate microcontroller's internal flash, OTP/eFuses,
RPMB, secure-element state, eMMC boot configuration, or UFS provisioning. Targets
must already have compatible provisioning and boot selection. Deploy MCU firmware
separately through the supported Arduino tooling. Omitting required boot LUNs
produces an incomplete board clone even though selected LUNs verify successfully.

## MCU programming and a ready indicator

Qualcomm EDL and the golden bundle cover application-processor storage. They do
not provide the normal MCU upload path. Arduino's UNO Q and VENTUNO Q board
profiles select `remoteocd`; the platform's upload recipe uses ADB to run the MCU
programming operation through Linux. That route is unavailable while the
application processor is in EDL. A separate SWD probe is a potential independent
route where debug access is available; this tool does not implement or validate it.

A complete production readiness phase should happen after Linux boots: deploy the
intended MCU firmware, verify it and its expected application handshake, then
signal readiness on the host and through a firmware-supported LED pattern. This
post-boot provisioning phase is not implemented yet. The current `verified`
result means the selected storage bytes match, not that MCU firmware or the
application has been deployed. Flashing a status-only sketch would replace the
intended MCU application and is not part of golden flashing.

References: [Arduino board profiles](https://github.com/arduino/ArduinoCore-zephyr/blob/main/boards.txt)
and [remoteocd upload recipes](https://github.com/arduino/ArduinoCore-zephyr/blob/main/platform.txt).

## Physical completion indication while still in EDL

The current EDL workflow has no supported board-LED control. A USB hub with true
per-port power switching could turn off a verified board and its port LED, provided
USB is the board's only power source. Logical USB disconnection alone does not
guarantee removal of power. Automatic hub control is not implemented.

The RSHTECH/Rosonway RSH-A16 (Amazon UK ASIN B08FDRZPDL) has different hardware
revisions. The Realtek revision (`0bda:0411` USB 3 / `0bda:5411` USB 2) was
reported to cut VBUS and extinguish blue LEDs on all 16 ports through `uhubctl`.
A VIA revision (`2109:0822` / `2109:2822`) was reported to cut power only on the
first six physical ports, with others disconnecting data only. The product name
alone is therefore insufficient to establish compatibility.

Before integrating power-off after verification, identify the actual hub and
test power removal on every intended port, including USB 2/USB 3 companion
mapping. Associate each board serial with a tested physical port and ensure a
switch cannot interrupt another active target or an upstream hub. These hardware
checks have not yet been performed for this fixture.

References: [original RSH-A16 report](https://github.com/mvp/uhubctl/issues/276),
[hardware revision discussion](https://github.com/mvp/uhubctl/issues/411), and
[maintainer's Realtek power and LED test](https://github.com/mvp/uhubctl/issues/411#issuecomment-1123045566).

## Validation record

On macOS arm64, 2026-11-04:

- All repository tests passed with the race detector, using `TMPDIR=/private/tmp`
  to avoid macOS temporary-directory alias differences in existing service tests.
- The actual embedded QDL v2.4-26 accepted generated write and read XMLs in its
  dry-run mode. To repeat this test, set `GOLDEN_TEST_QDL` to an absolute QDL path
  and run `go test -race ./internal/golden`.
- Automatic programmer download/cache reuse passed with a simulated HTTP server.
  Selective extraction from both official archives (UNO Q `20260528-558` and
  VENTUNO Q `20260729-248`) matched the SHA-256 hashes of the previously extracted
  programmers. This checks archive compatibility, not VENTUNO Q hardware.
- A physical UNO Q in EDL completed a full 15,675,162,624-byte capture with the
  official `20260528-558` programmer. A subsequent CLI dry-run rechecked the bundle
  and produced a two-target plan without USB access.
- The same UNO Q was restored with explicit source-recovery authorization; every
  readback chunk matched. Capture took 2m 44s and verified restore took 16m 31s.
  Normal boot completed in 30.403s, with no failed systemd units.
  See [the UNO Q validation record](unoq-validation.md) for phase timings and
  remaining checks. The later [three-board batch](unoq-three-board-validation.md)
  verified all three targets; post-flash boot was not reported for that batch.
