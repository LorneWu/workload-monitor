// Package procfs reads Linux performance counters directly from /proc and
// /sys, with no subprocess spawning, so the sampler itself has negligible
// CPU/memory overhead even when run continuously for hours.
package procfs

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// CPUTimes holds the raw jiffies for one CPU line (aggregate "cpu" or a
// specific "cpuN") from /proc/stat.
type CPUTimes struct {
	User, Nice, System, Idle, IOWait, IRQ, SoftIRQ, Steal int64
}

func (c CPUTimes) Total() int64 {
	return c.User + c.Nice + c.System + c.Idle + c.IOWait + c.IRQ + c.SoftIRQ + c.Steal
}

// ReadCPUStat returns the aggregate "cpu" line and each "cpuN" line from
// /proc/stat, keyed by core index.
func ReadCPUStat() (agg CPUTimes, perCore map[int]CPUTimes, err error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return agg, nil, err
	}
	defer f.Close()
	perCore = make(map[int]CPUTimes)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "cpu") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 9 {
			continue
		}
		vals := make([]int64, 8)
		for i := 0; i < 8; i++ {
			vals[i], _ = strconv.ParseInt(fields[i+1], 10, 64)
		}
		ct := CPUTimes{vals[0], vals[1], vals[2], vals[3], vals[4], vals[5], vals[6], vals[7]}
		if fields[0] == "cpu" {
			agg = ct
		} else {
			idx, e := strconv.Atoi(strings.TrimPrefix(fields[0], "cpu"))
			if e == nil {
				perCore[idx] = ct
			}
		}
	}
	return agg, perCore, sc.Err()
}

// MemInfo holds the /proc/meminfo fields relevant for spotting memory
// pressure: raw usage, and the two strongest pressure signals — dirty pages
// awaiting writeback, and pages actually being written back right now.
type MemInfo struct {
	TotalKB, AvailableKB, SwapTotalKB, SwapFreeKB, DirtyKB, WritebackKB int64
}

func ReadMemInfo() (MemInfo, error) {
	var m MemInfo
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return m, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		v, _ := strconv.ParseInt(fields[1], 10, 64)
		switch strings.TrimSuffix(fields[0], ":") {
		case "MemTotal":
			m.TotalKB = v
		case "MemAvailable":
			m.AvailableKB = v
		case "SwapTotal":
			m.SwapTotalKB = v
		case "SwapFree":
			m.SwapFreeKB = v
		case "Dirty":
			m.DirtyKB = v
		case "Writeback":
			m.WritebackKB = v
		}
	}
	return m, sc.Err()
}

// VMStat holds cumulative swap-activity counters from /proc/vmstat. A
// nonzero delta between two samples means the kernel is actually thrashing,
// not just "using a lot of RAM" — a much stronger pressure signal than
// MemInfo alone.
type VMStat struct {
	PSwpIn, PSwpOut int64
}

func ReadVMStat() (VMStat, error) {
	var v VMStat
	f, err := os.Open("/proc/vmstat")
	if err != nil {
		return v, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		n, _ := strconv.ParseInt(fields[1], 10, 64)
		switch fields[0] {
		case "pswpin":
			v.PSwpIn = n
		case "pswpout":
			v.PSwpOut = n
		}
	}
	return v, sc.Err()
}

// LoadAvg returns the 1-minute load average from /proc/loadavg.
func LoadAvg1() (float64, error) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(b))
	if len(fields) < 1 {
		return 0, nil
	}
	f, _ := strconv.ParseFloat(fields[0], 64)
	return f, nil
}

// NetDev holds cumulative rx/tx byte counters for one interface from
// /proc/net/dev.
type NetDev struct {
	RxBytes, TxBytes int64
}

func ReadNetDev(iface string) (NetDev, error) {
	var n NetDev
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return n, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	prefix := iface + ":"
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, prefix))
		fields := strings.Fields(rest)
		// layout: rx_bytes rx_packets rx_errs rx_drop rx_fifo rx_frame
		// rx_compressed rx_multicast tx_bytes tx_packets ...
		if len(fields) < 9 {
			continue
		}
		n.RxBytes, _ = strconv.ParseInt(fields[0], 10, 64)
		n.TxBytes, _ = strconv.ParseInt(fields[8], 10, 64)
	}
	return n, sc.Err()
}

// TCPRetrans returns the cumulative RetransSegs counter from
// /proc/net/snmp's "Tcp:" section — sustained retransmits indicate the
// network path itself is struggling, distinct from raw throughput looking
// fine.
func ReadTCPRetrans() (int64, error) {
	f, err := os.Open("/proc/net/snmp")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var header, values []string
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "Tcp:") {
			if header == nil {
				header = strings.Fields(line)
			} else {
				values = strings.Fields(line)
				break
			}
		}
	}
	for i, name := range header {
		if name == "RetransSegs" && i < len(values) {
			v, _ := strconv.ParseInt(values[i], 10, 64)
			return v, nil
		}
	}
	return 0, nil
}

// DiskStats holds cumulative counters for one block device from
// /proc/diskstats.
type DiskStats struct {
	SectorsRead, SectorsWritten     int64
	ReadsCompleted, WritesCompleted int64
	TimeReadingMs, TimeWritingMs    int64
	TimeIOMs                        int64
}

func ReadDiskStats(device string) (DiskStats, error) {
	var d DiskStats
	f, err := os.Open("/proc/diskstats")
	if err != nil {
		return d, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 14 || fields[2] != device {
			continue
		}
		d.ReadsCompleted, _ = strconv.ParseInt(fields[3], 10, 64)
		d.SectorsRead, _ = strconv.ParseInt(fields[5], 10, 64)
		d.TimeReadingMs, _ = strconv.ParseInt(fields[6], 10, 64)
		d.WritesCompleted, _ = strconv.ParseInt(fields[7], 10, 64)
		d.SectorsWritten, _ = strconv.ParseInt(fields[9], 10, 64)
		d.TimeWritingMs, _ = strconv.ParseInt(fields[10], 10, 64)
		d.TimeIOMs, _ = strconv.ParseInt(fields[12], 10, 64)
		break
	}
	return d, sc.Err()
}

// MaxThermalC returns the highest reading, in Celsius, across every zone
// under /sys/class/thermal/thermal_zone*/temp. Zone numbering isn't
// standardized across machines, so rather than requiring the caller to know
// which zone is "the CPU" on this particular board, we just report whatever
// is hottest — good enough to catch thermal throttling regardless of board
// layout.
func MaxThermalC() (float64, bool) {
	entries, err := filepath.Glob("/sys/class/thermal/thermal_zone*/temp")
	if err != nil {
		return 0, false
	}
	var max float64
	found := false
	for _, p := range entries {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		milliC, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
		if err != nil {
			continue
		}
		c := float64(milliC) / 1000.0
		if !found || c > max {
			max = c
			found = true
		}
	}
	return max, found
}

// AvgCPUFreqMHz returns the average current scaling frequency across all
// CPU cores, in MHz — a low average under high load is a direct sign of
// thermal or power throttling that raw CPU% alone won't reveal.
func AvgCPUFreqMHz() (float64, bool) {
	entries, err := filepath.Glob("/sys/devices/system/cpu/cpu[0-9]*/cpufreq/scaling_cur_freq")
	if err != nil || len(entries) == 0 {
		return 0, false
	}
	var sum float64
	n := 0
	for _, p := range entries {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		khz, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
		if err != nil {
			continue
		}
		sum += khz / 1000.0
		n++
	}
	if n == 0 {
		return 0, false
	}
	return sum / float64(n), true
}

// CPUMaxFreqMHz reads the hardware's rated maximum frequency
// (cpuinfo_max_freq, static for the machine's lifetime) from cpu0. Compared
// against AvgCPUFreqMHz, this is the reference point for spotting
// throttling on ANY vendor: actual frequency sitting well below this while
// cpu_pct is high is the cross-platform tell, needed on AMD where
// ThrottleCounts below isn't available.
func CPUMaxFreqMHz() (float64, bool) {
	b, err := os.ReadFile("/sys/devices/system/cpu/cpu0/cpufreq/cpuinfo_max_freq")
	if err != nil {
		return 0, false
	}
	khz, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
	if err != nil {
		return 0, false
	}
	return khz / 1000.0, true
}

// ThrottleCounts reads Intel's cumulative thermal-throttle event counters
// from /sys/devices/system/cpu/cpuN/thermal_throttle/ — core_throttle_count
// summed across all cores, package_throttle_count from cpu0 (package-wide,
// identical across cores so one read suffices). This is a DIRECT signal
// ("the CPU itself reports it throttled N times"), not an inference from
// frequency — far more conclusive than watching AvgCPUFreqMHz dip.
//
// This interface is Intel-specific; AMD exposes no equivalent standard
// sysfs counter. ok=false means "unsupported on this CPU," never "zero
// throttle events" — callers must not treat it as a clean bill of health.
func ThrottleCounts() (coreCount, packageCount int64, ok bool) {
	entries, err := filepath.Glob("/sys/devices/system/cpu/cpu[0-9]*/thermal_throttle/core_throttle_count")
	if err != nil || len(entries) == 0 {
		return 0, 0, false
	}
	for _, p := range entries {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
		if err == nil {
			coreCount += n
		}
	}
	if pb, err := os.ReadFile("/sys/devices/system/cpu/cpu0/thermal_throttle/package_throttle_count"); err == nil {
		packageCount, _ = strconv.ParseInt(strings.TrimSpace(string(pb)), 10, 64)
	}
	return coreCount, packageCount, true
}

// DefaultInterface makes a best-effort guess at the primary network
// interface by reading the default route from /proc/net/route (the
// interface whose destination is 0.0.0.0).
func DefaultInterface() (string, bool) {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return "", false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	first := true
	for sc.Scan() {
		if first { // header
			first = false
			continue
		}
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		if fields[1] == "00000000" {
			return fields[0], true
		}
	}
	return "", false
}

// DefaultDisk finds the physical block device that actually backs "/", by
// reading /proc/mounts for the root filesystem's source device and, if
// that's an LVM/device-mapper node, resolving through
// /sys/block/<dm>/slaves/ down to the real disk underneath.
//
// This deliberately does NOT just pick "the first non-loop/dm entry under
// /sys/block" — on a machine with more than one physical disk (e.g. a spare
// NVMe alongside the SATA disk actually hosting the filesystem being
// profiled), alphabetical or directory-order picking can silently choose
// the wrong, idle device and report all-zero disk I/O for an entire run.
func DefaultDisk() (string, bool) {
	dev, ok := rootMountSource()
	if !ok {
		return firstPhysicalBlockDevice()
	}
	name := filepath.Base(dev)
	// Resolve dm-N (LVM, luks, etc.) down to the underlying physical device.
	for i := 0; i < 5; i++ { // bounded in case of unexpected nesting
		slaves, err := os.ReadDir("/sys/block/" + name + "/slaves")
		if err != nil || len(slaves) == 0 {
			break
		}
		name = slaves[0].Name()
	}
	// Strip a trailing partition number (sda3 -> sda, nvme0n1p3 -> nvme0n1)
	// if /sys/block has the whole-disk entry but not the partition itself.
	if _, err := os.Stat("/sys/block/" + name); err != nil {
		name = stripPartitionSuffix(name)
	}
	if _, err := os.Stat("/sys/block/" + name); err != nil {
		return firstPhysicalBlockDevice()
	}
	return name, true
}

// rootMountSource returns the device field for the "/" entry in
// /proc/mounts.
func rootMountSource() (string, bool) {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return "", false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 || fields[1] != "/" {
			continue
		}
		if !strings.HasPrefix(fields[0], "/dev/") {
			return "", false
		}
		// /dev/mapper/foo is a symlink to the real /dev/dm-N node.
		if resolved, err := filepath.EvalSymlinks(fields[0]); err == nil {
			return resolved, true
		}
		return fields[0], true
	}
	return "", false
}

var partitionSuffix = regexp.MustCompile(`(p?\d+)$`)

func stripPartitionSuffix(name string) string {
	return partitionSuffix.ReplaceAllString(name, "")
}

// firstPhysicalBlockDevice is the fallback used when /proc/mounts can't be
// read or resolved: the first /sys/block entry that isn't a loop device,
// ram disk, device-mapper node, or optical drive.
func firstPhysicalBlockDevice() (string, bool) {
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") ||
			strings.HasPrefix(name, "dm-") || strings.HasPrefix(name, "sr") {
			continue
		}
		return name, true
	}
	return "", false
}
