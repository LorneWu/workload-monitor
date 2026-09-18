#!/bin/bash
# Example: profile a full DSM build cycle (pull code -> BaseAll -> Snapshot.py
# -> SynoUpdate -> BuildAll) stage by stage. Requires `monitor` and `stage`
# binaries on PATH (see README for build/install instructions), and a
# root/sudo-capable account for the DSM build tooling itself.
set -u
PDIR=/synosrc/YOUR_PROJECT_DIR
BRANCH=your-branch
PLATFORM=your-platform
PROJ=your-project

OUTDIR=~/workload-monitor-run-$(date +%Y%m%d-%H%M%S)
mkdir -p "$OUTDIR"
SAMPLES="$OUTDIR/samples.csv"
EVENTS="$OUTDIR/events.csv"

PWFILE="$HOME/.buildpw_tmp"   # write your sudo password here, mode 600, once
PW=$(cat "$PWFILE")
S() { printf '%s\n' "$PW" | sudo -S -p "" "$@"; }

cleanup_tree() {
  for m in $(mount | awk -v p="$PDIR" 'index($3,p)==1 {print $3}' | sort -r); do
    S umount "$m" || true
  done
  S rm -rf "$PDIR"
}

# Start the sampler in the background. -iface/-disk auto-detect if omitted;
# pass them explicitly if auto-detection picks the wrong NIC/disk on your box.
monitor -out "$SAMPLES" &
MONITOR_PID=$!
trap 'kill $MONITOR_PID 2>/dev/null' EXIT

cleanup_tree

stage "$EVENTS" start pull_code
S mkdir -p "$PDIR/ds.base"
cd "$PDIR/ds.base"
S git clone git@git.synology.inc:synology/lnxscripts.git
S git -C lnxscripts checkout "$BRANCH"
cd "$PDIR"
for f in BaseAll BuildAll Snapshot.py SynoUpdate; do
  S ln -sfn "ds.base/lnxscripts/$f" "$f"
done
stage "$EVENTS" end pull_code

stage "$EVENTS" start BaseAll
S mkdir -p "ds.$PLATFORM"
S ./BaseAll -f -p "$PLATFORM"
stage "$EVENTS" end BaseAll

stage "$EVENTS" start Snapshot
S ./Snapshot.py -y -p "$PLATFORM" -m
stage "$EVENTS" end Snapshot

stage "$EVENTS" start SynoUpdate
S ./SynoUpdate "$PROJ"
stage "$EVENTS" end SynoUpdate

stage "$EVENTS" start BuildAll
S ./BuildAll -UF -p "$PLATFORM" "$PROJ"
stage "$EVENTS" end BuildAll

kill "$MONITOR_PID" 2>/dev/null
trap - EXIT

echo "Done. Report with:"
echo "  report -samples $SAMPLES -events $EVENTS"
