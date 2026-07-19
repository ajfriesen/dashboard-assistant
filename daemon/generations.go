package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// systemProfile is the NixOS system profile; its numbered "-N-link" siblings are
// the bootable generations, and the symlink itself points at the current one.
// All live under /nix, which is world-readable, so the unprivileged daemon can
// enumerate them (rolling back needs root — see bootGeneration / ha-rollback@).
const systemProfile = "/nix/var/nix/profiles/system"

// Generation is one bootable NixOS system generation.
type Generation struct {
	Number  int    `json:"number"`
	Date    string `json:"date"` // activation time (profile symlink mtime), RFC3339
	Current bool   `json:"current"`
}

// listGenerations enumerates the system generations, newest first, marking the
// one the profile currently points at.
func listGenerations() ([]Generation, error) {
	current := currentGeneration()
	matches, err := filepath.Glob(systemProfile + "-*-link")
	if err != nil {
		return nil, err
	}
	gens := make([]Generation, 0, len(matches))
	for _, m := range matches {
		n := genNumber(m)
		if n <= 0 {
			continue
		}
		date := ""
		if fi, err := os.Lstat(m); err == nil {
			date = fi.ModTime().Format(time.RFC3339)
		}
		gens = append(gens, Generation{Number: n, Date: date, Current: n == current})
	}
	sort.Slice(gens, func(i, j int) bool { return gens[i].Number > gens[j].Number })
	return gens, nil
}

// currentGeneration is the number the profile symlink points at, or 0.
func currentGeneration() int {
	target, err := os.Readlink(systemProfile)
	if err != nil {
		return 0
	}
	return genNumber(target)
}

// genNumber extracts N from a "system-N-link" name or path.
func genNumber(s string) int {
	base := filepath.Base(s)
	base = strings.TrimPrefix(base, "system-")
	base = strings.TrimSuffix(base, "-link")
	n, err := strconv.Atoi(base)
	if err != nil {
		return 0
	}
	return n
}

// generationExists reports whether generation n has a profile link.
func generationExists(n int) bool {
	_, err := os.Lstat(fmt.Sprintf("%s-%d-link", systemProfile, n))
	return err == nil
}
