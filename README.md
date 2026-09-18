# workload-monitor

Lightweight, single-process resource profiler for comparing build/workload
performance across machines, stage by stage. Built to profile DSM kernel
builds on old/loaned dev boxes, but the sampler itself is generic.

## Why not just `sar`/`iostat`/`dstat`?

Those are fine tools, but composing "per-stage avg/max across CPU, memory,
network, disk, temperature, and frequency, compared across two machines"
out of them means gluing several tools + manual timestamp correlation by
hand — which is what this repo replaces with one static binary and one
report command.

Design goals, in order:

1. **Near-zero overhead.** `monitor` is a single Go process that reads
   `/proc` and `/sys` directly every tick — it never forks a subprocess
   (no `awk`, `ps`, `date`, etc. per sample). Safe to leave running for a
   multi-hour build without meaningfully affecting the numbers it's
   measuring.
2. **No install step on the target machine.** `monitor`/`stage`/`report`
   are static binaries — `scp` and run. No Python/awk version concerns, no
   package installs on machines you don't control.
3. **Stage-aware.** Wrap each phase of your build (`BaseAll`, `Snapshot.py`,
   `SynoUpdate`, `BuildAll`, or anything else) with `stage <events.csv>
   start|end <name>`; `report` correlates those markers against the
   sampler's timeline automatically.
4. **Cross-machine comparison built in.** `report -samples2 ... -events2
   ...` prints a side-by-side table, not just two separate reports you have
   to diff yourself.

## What it measures, and why each metric is there

| Metric | Why it matters |
|---|---|
| `cpu_pct` | overall busy % (100 − idle) |
| `cpu_user_pct` | busy % from actual compute (user+nice+system) — see below |
| `cpu_iowait_pct` | % of time the CPU was stalled waiting on I/O, **not** doing work. `cpu_pct` alone lumps this in as "busy," which makes I/O-bound stages look falsely CPU-bound. Compare `cpu_user_pct` vs `cpu_iowait_pct` to tell which one it actually is. |
| `cpu_core_max_pct` | busiest single core. A system-wide average near 50% on a 4-core box could mean "everything's evenly loaded" or "one core is pegged at 100% (e.g. the final link step) while the others idle" — this tells them apart. |
| `load1` | 1-minute load average, for a quick sanity check against core count |
| `mem_used_mb` / `mem_avail_mb` | basic memory usage |
| `swap_used_mb` | how much swap is currently occupied |
| `swap_in_kBps` / `swap_out_kBps` | **actual thrashing**, not just "using a lot of RAM." A high `mem_used_mb` with zero swap activity is fine; nonzero `swap_out_kBps` means real memory pressure. |
| `dirty_mb` / `writeback_mb` | pages queued to be flushed to disk / currently being flushed. Directly relevant to silent-corruption-style bugs that only manifest under heavy buffered-write pressure. |
| `net_rx_kBps` / `net_tx_kBps` | network throughput |
| `tcp_retrans_ps` | TCP retransmits/sec — network-layer struggle that raw throughput numbers can hide |
| `disk_read_kBps` / `disk_write_kBps` | disk throughput |
| `disk_util_pct` | % of the sample interval the disk had at least one I/O in flight |
| `disk_await_ms` | average time per I/O request — a better "is the disk the bottleneck" signal than throughput, since a saturated queue can still show OK throughput while every request waits |
| `temp_c` | hottest reading across every `/sys/class/thermal/thermal_zone*` — thermal throttling is invisible to every other metric here, and was the actual root cause the last time this mattered |
| `cpu_freq_mhz` | average current CPU clock — a low reading under high load is the direct fingerprint of thermal/power throttling |

## Build

Requires a Go toolchain (no root needed — the official tarball extracts
anywhere):

```bash
go build -o monitor ./cmd/monitor
go build -o stage   ./cmd/stage
go build -o report  ./cmd/report
```

Or cross-compile for a remote box and `scp` the binaries over — no Go
install needed on the target:

```bash
GOOS=linux GOARCH=amd64 go build -o monitor ./cmd/monitor
scp monitor stage user@target:~/
```

## Usage

Start the sampler (auto-detects the default-route interface and the first
physical disk if you don't pass `-iface`/`-disk`):

```bash
./monitor -out samples.csv &
MONITOR_PID=$!
```

Bracket each phase of your workload:

```bash
./stage events.csv start BaseAll
./BaseAll -f -p epyc7003ntb
./stage events.csv end BaseAll

./stage events.csv start BuildAll
./BuildAll -UF -p epyc7003ntb linux-5.10.x
./stage events.csv end BuildAll
```

Stop the sampler, then report:

```bash
kill $MONITOR_PID
./report -samples samples.csv -events events.csv
```

Compare two runs (e.g. two different machines that both ran
`examples/dsm-build.sh`):

```bash
./report -samples hostA/samples.csv -events hostA/events.csv \
          -samples2 hostB/samples.csv -events2 hostB/events.csv \
          -label1 hostA -label2 hostB
```

See `examples/dsm-build.sh` for a full worked example (a DSM `BaseAll` /
`Snapshot.py` / `SynoUpdate` / `BuildAll` cycle, staged and profiled).

## `monitor` flags

| Flag | Default | Meaning |
|---|---|---|
| `-iface` | auto-detect (default route interface) | network interface to sample |
| `-disk` | auto-detect (first non-loop/ram/dm/sr block device) | block device to sample |
| `-interval` | `2s` | sampling interval |
| `-out` | stdout | output CSV path |

## `report` flags

| Flag | Meaning |
|---|---|
| `-samples`, `-events` | first (or only) run's files |
| `-samples2`, `-events2` | second run's files — presence of both enables comparison mode |
| `-label1`, `-label2` | labels for the two runs in comparison output (default `run1`/`run2`) |
