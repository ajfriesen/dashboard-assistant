package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// readMem returns total and used physical memory in MiB. "Used" is
// total - available (MemAvailable, which discounts reclaimable cache), matching
// what `free` reports as used — the number people actually mean by memory usage.
func readMem() (totalMiB, usedMiB int, err error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	var total, avail int64
	haveTotal, haveAvail := false, false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text()) // e.g. "MemTotal: 2048000 kB"
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			total, _ = strconv.ParseInt(fields[1], 10, 64)
			haveTotal = true
		case "MemAvailable:":
			avail, _ = strconv.ParseInt(fields[1], 10, 64)
			haveAvail = true
		}
	}
	if err := sc.Err(); err != nil {
		return 0, 0, err
	}
	if !haveTotal || !haveAvail {
		return 0, 0, fmt.Errorf("meminfo: MemTotal/MemAvailable missing")
	}
	// /proc/meminfo is in kB (KiB); divide by 1024 for MiB.
	return int(total / 1024), int((total - avail) / 1024), nil
}

// readDisk returns total and used space of the root filesystem in GiB. "Used" is
// total - free (what `df` reports as used), on the grown-to-fill root partition.
func readDisk() (totalGiB, usedGiB float64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs("/", &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize)
	total := st.Blocks * bs
	used := (st.Blocks - st.Bfree) * bs
	const gib = 1024 * 1024 * 1024
	return float64(total) / gib, float64(used) / gib, nil
}
