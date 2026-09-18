// Command monitor samples system-wide CPU, memory, network, disk, thermal
// and frequency counters at a fixed interval and writes them as CSV — one
// process, no subprocess forking, so the sampler's own footprint stays
// negligible even across a many-hour run.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/LorneWu/workload-monitor/internal/procfs"
)

func main() {
	iface := flag.String("iface", "", "network interface to sample (default: auto-detect default route interface)")
	disk := flag.String("disk", "", "block device to sample, e.g. sda (default: auto-detect first physical disk)")
	interval := flag.Duration("interval", 2*time.Second, "sampling interval")
	out := flag.String("out", "", "output CSV path (default: stdout)")
	flag.Parse()

	if *iface == "" {
		if d, ok := procfs.DefaultInterface(); ok {
			*iface = d
		} else {
			log.Fatal("could not auto-detect network interface; pass -iface explicitly")
		}
	}
	if *disk == "" {
		if d, ok := procfs.DefaultDisk(); ok {
			*disk = d
		} else {
			log.Fatal("could not auto-detect disk device; pass -disk explicitly")
		}
	}

	var w *os.File
	if *out == "" {
		w = os.Stdout
	} else {
		f, err := os.Create(*out)
		if err != nil {
			log.Fatalf("create output: %v", err)
		}
		defer f.Close()
		w = f
	}

	fmt.Fprintln(w, "epoch,cpu_pct,cpu_user_pct,cpu_iowait_pct,cpu_core_max_pct,load1,"+
		"mem_used_mb,mem_avail_mb,swap_used_mb,swap_in_kBps,swap_out_kBps,dirty_mb,writeback_mb,"+
		"net_rx_kBps,net_tx_kBps,tcp_retrans_ps,"+
		"disk_read_kBps,disk_write_kBps,disk_util_pct,disk_await_ms,"+
		"temp_c,cpu_freq_mhz")
	_ = w.Sync()

	prevAgg, prevCores, err := procfs.ReadCPUStat()
	if err != nil {
		log.Fatalf("read /proc/stat: %v", err)
	}
	prevNet, _ := procfs.ReadNetDev(*iface)
	prevDisk, _ := procfs.ReadDiskStats(*disk)
	prevVM, _ := procfs.ReadVMStat()
	prevRetrans, _ := procfs.ReadTCPRetrans()
	prevTime := time.Now()

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()
		elapsed := now.Sub(prevTime).Seconds()
		if elapsed <= 0 {
			elapsed = float64(*interval) / float64(time.Second)
		}

		agg, cores, err := procfs.ReadCPUStat()
		if err != nil {
			continue
		}
		cpuPct, userPct, iowaitPct := cpuPercents(prevAgg, agg)
		coreMaxPct := maxCorePct(prevCores, cores)

		load1, _ := procfs.LoadAvg1()

		mem, _ := procfs.ReadMemInfo()
		memUsedMB := float64(mem.TotalKB-mem.AvailableKB) / 1024
		memAvailMB := float64(mem.AvailableKB) / 1024
		swapUsedMB := float64(mem.SwapTotalKB-mem.SwapFreeKB) / 1024
		dirtyMB := float64(mem.DirtyKB) / 1024
		writebackMB := float64(mem.WritebackKB) / 1024

		vm, _ := procfs.ReadVMStat()
		swapInKBps := float64(vm.PSwpIn-prevVM.PSwpIn) * 4 / elapsed // page size 4KB on x86_64
		swapOutKBps := float64(vm.PSwpOut-prevVM.PSwpOut) * 4 / elapsed

		net, _ := procfs.ReadNetDev(*iface)
		rxKBps := float64(net.RxBytes-prevNet.RxBytes) / 1024 / elapsed
		txKBps := float64(net.TxBytes-prevNet.TxBytes) / 1024 / elapsed

		retrans, _ := procfs.ReadTCPRetrans()
		retransPs := float64(retrans-prevRetrans) / elapsed

		ds, _ := procfs.ReadDiskStats(*disk)
		readKBps := float64(ds.SectorsRead-prevDisk.SectorsRead) * 512 / 1024 / elapsed
		writeKBps := float64(ds.SectorsWritten-prevDisk.SectorsWritten) * 512 / 1024 / elapsed
		utilPct := float64(ds.TimeIOMs-prevDisk.TimeIOMs) / (elapsed * 1000) * 100
		if utilPct > 100 {
			utilPct = 100
		}
		diskIOs := (ds.ReadsCompleted - prevDisk.ReadsCompleted) + (ds.WritesCompleted - prevDisk.WritesCompleted)
		diskTimeMs := (ds.TimeReadingMs - prevDisk.TimeReadingMs) + (ds.TimeWritingMs - prevDisk.TimeWritingMs)
		var awaitMs float64
		if diskIOs > 0 {
			awaitMs = float64(diskTimeMs) / float64(diskIOs)
		}

		tempC, _ := procfs.MaxThermalC()
		freqMHz, _ := procfs.AvgCPUFreqMHz()

		fmt.Fprintf(w, "%d,%.1f,%.1f,%.1f,%.1f,%.2f,%.0f,%.0f,%.0f,%.1f,%.1f,%.1f,%.1f,%.1f,%.1f,%.1f,%.1f,%.1f,%.1f,%.1f,%.1f,%.0f\n",
			now.Unix(), cpuPct, userPct, iowaitPct, coreMaxPct, load1,
			memUsedMB, memAvailMB, swapUsedMB, swapInKBps, swapOutKBps, dirtyMB, writebackMB,
			rxKBps, txKBps, retransPs,
			readKBps, writeKBps, utilPct, awaitMs,
			tempC, freqMHz)
		_ = w.Sync()

		prevAgg, prevCores = agg, cores
		prevNet = net
		prevDisk = ds
		prevVM = vm
		prevRetrans = retrans
		prevTime = now
	}
}

// cpuPercents returns (busy%, user+system%, iowait%) between two aggregate
// CPU samples. "busy" counts everything except idle; iowait is reported
// separately since it means the CPU was stalled waiting on I/O, not
// actually computing — lumping it into "busy" (a common mistake) makes
// I/O-bound stages look CPU-bound.
func cpuPercents(prev, cur procfs.CPUTimes) (busyPct, userPct, iowaitPct float64) {
	dtotal := cur.Total() - prev.Total()
	if dtotal <= 0 {
		return 0, 0, 0
	}
	didle := cur.Idle - prev.Idle
	duser := (cur.User - prev.User) + (cur.Nice - prev.Nice) + (cur.System - prev.System)
	diowait := cur.IOWait - prev.IOWait
	busyPct = (1 - float64(didle)/float64(dtotal)) * 100
	userPct = float64(duser) / float64(dtotal) * 100
	iowaitPct = float64(diowait) / float64(dtotal) * 100
	return
}

// maxCorePct returns the busiest single core's utilization between two
// samples — a system-wide average can hide a single-threaded bottleneck
// (e.g. the final link step) where one core is pegged at 100% while others
// sit idle.
func maxCorePct(prev, cur map[int]procfs.CPUTimes) float64 {
	var max float64
	for idx, c := range cur {
		p, ok := prev[idx]
		if !ok {
			continue
		}
		dtotal := c.Total() - p.Total()
		if dtotal <= 0 {
			continue
		}
		didle := c.Idle - p.Idle
		pct := (1 - float64(didle)/float64(dtotal)) * 100
		if pct > max {
			max = pct
		}
	}
	return max
}
