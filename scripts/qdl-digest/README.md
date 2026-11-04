# Storage digest QDL helper

This helper extends [upstream QDL v2.4](https://github.com/linux-msm/qdl/tree/v2.4)
with an explicit `<getsha256digest>` read operation. It requires a programmer
that implements the Firehose storage digest command. VIP digest tables are unrelated.

Build on macOS or Linux with a C compiler, `make`, `patch`, `curl`, Python 3,
`pkg-config`, libxml2 and libusb installed:

```sh
scripts/qdl-digest/build.sh
```

The build verifies the pinned source archive SHA-256, applies the checked-in
patch and tests the actual C parser before compiling `build/qdl-digest`.
The helper uses native shared libraries; build it on each host rather than copying
it between operating systems. The standard embedded QDL is unchanged. Windows
helper packaging is not provided; readback verification remains supported there.
The extracted upstream source includes its BSD-3-Clause license.

Each digest output contains exactly 64 hexadecimal characters. An ACK alone,
missing/malformed/duplicate digest, NAK or failed output write fails the operation.
The CLI independently validates every output and compares every required range.
The command timeout is five minutes per range, while the CLI enforces its overall
per-target timeout and kills QDL on cancellation. Golden ranges are at most 1 GiB.

Example helper XML (read-only):

```xml
<data>
  <getsha256digest SECTOR_SIZE_IN_BYTES="512" physical_partition_number="0"
    start_sector="0" num_partition_sectors="2048" filename="first.sha256"/>
</data>
```

The existing `read` and `program` operations retain their behavior. Digest-mode
flashing uses the embedded QDL for probing and writing, and this helper only for
storage digests. The filename has no special meaning; the explicit XML tag selects
hashing. No helper is built or downloaded automatically during flashing.
