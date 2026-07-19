package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
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

// readUptime returns whole seconds since boot from /proc/uptime.
func readUptime() (int, error) {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, err
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0, fmt.Errorf("uptime: empty")
	}
	sec, err := strconv.ParseFloat(f[0], 64)
	return int(sec), err
}

// cpu is a package-level CPU-usage sampler (kept outside the MQTT bridge so it
// survives reconnects and measures over the telemetry interval between calls).
var cpu = &cpuSampler{}

type cpuSampler struct {
	mu                  sync.Mutex
	prevIdle, prevTotal uint64
}

// Usage returns busy CPU percent since the previous call (0 on the first call).
func (c *cpuSampler) Usage() (int, error) {
	idle, total, err := readCPUTimes()
	if err != nil {
		return 0, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	dt := total - c.prevTotal
	di := idle - c.prevIdle
	c.prevIdle, c.prevTotal = idle, total
	if dt == 0 {
		return 0, nil
	}
	return int(100 * (dt - di) / dt), nil
}

// readCPUTimes sums the aggregate "cpu" line of /proc/stat: idle counts idle +
// iowait, total counts every field.
func readCPUTimes() (idle, total uint64, err error) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, err
	}
	line, _, _ := strings.Cut(string(b), "\n")
	f := strings.Fields(line) // cpu user nice system idle iowait irq softirq steal ...
	if len(f) < 6 || f[0] != "cpu" {
		return 0, 0, fmt.Errorf("cpu: bad /proc/stat")
	}
	for i := 1; i < len(f); i++ {
		v, _ := strconv.ParseUint(f[i], 10, 64)
		total += v
		if i == 4 || i == 5 { // idle, iowait
			idle += v
		}
	}
	return idle, total, nil
}

// primaryIP is the source address the kernel would use to reach the network —
// the device's main LAN IP. Empty when offline.
func primaryIP() string {
	if c, err := net.Dial("udp", "1.1.1.1:80"); err == nil { // no packets sent; just picks a source
		defer c.Close()
		if a, ok := c.LocalAddr().(*net.UDPAddr); ok {
			return a.IP.String()
		}
	}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() {
			if v4 := n.IP.To4(); v4 != nil {
				return v4.String()
			}
		}
	}
	return ""
}

func hostname() string {
	if h, err := os.Hostname(); err == nil {
		return h
	}
	return "unknown"
}

func readDMI(field string) string {
	b, err := os.ReadFile("/sys/class/dmi/id/" + field)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// readModel is the DMI board model, e.g. "HARDKERNEL ODROID-H2" (both fields are
// world-readable, unlike the serial).
func readModel() string {
	name := readDMI("product_name")
	vendor := readDMI("sys_vendor")
	switch {
	case name == "":
		return vendor
	case vendor == "" || strings.Contains(strings.ToLower(name), strings.ToLower(vendor)):
		return name
	default:
		return vendor + " " + name
	}
}

// readSerial returns the hardware serial the root ExecStartPre helper stashed in
// dmiFile (it filters junk BIOS placeholders), falling back to the machine-id —
// a stable unique device id — when there's no real serial.
func readSerial() string {
	if m, err := parseEnvFile(dmiFile); err == nil {
		if s := strings.TrimSpace(m["SERIAL"]); s != "" {
			return s
		}
	}
	if b, err := os.ReadFile("/etc/machine-id"); err == nil {
		return strings.TrimSpace(string(b))
	}
	return "unknown"
}
