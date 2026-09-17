package driver

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// clockTicks is USER_HZ, which Linux fixes at 100 for /proc on every
// architecture Go releases for.
const clockTicks = 100

// procStat is the part of /proc/<pid>/stat the one-owner rule reads.
type procStat struct {
	state byte
	pgrp  int
	ticks int64
}

func readProcStat(pid int) (procStat, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return procStat{}, err
	}
	// The command name is parenthesized and may hold spaces or parentheses;
	// the fields after the last ')' are fixed.
	end := strings.LastIndexByte(string(raw), ')')
	if end < 0 {
		return procStat{}, errors.New("driver: unreadable /proc stat")
	}
	fields := strings.Fields(string(raw)[end+1:])
	// fields[0] is the state (field 3), fields[2] the process group (field
	// 5), fields[19] the start time (field 22).
	if len(fields) < 20 || len(fields[0]) != 1 {
		return procStat{}, errors.New("driver: short /proc stat")
	}
	pgrp, err := strconv.Atoi(fields[2])
	if err != nil {
		return procStat{}, fmt.Errorf("driver: /proc stat pgrp: %w", err)
	}
	ticks, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil {
		return procStat{}, fmt.Errorf("driver: /proc stat starttime: %w", err)
	}
	return procStat{state: fields[0][0], pgrp: pgrp, ticks: ticks}, nil
}

// processStartTime is when the kernel started pid: /proc/<pid>/stat's
// starttime, in ticks since boot, plus the boot time from /proc/stat. A
// zombie is a process that is gone: it runs nothing, and only its parent's
// wait is left of it.
func processStartTime(pid int) (time.Time, error) {
	st, err := readProcStat(pid)
	if err != nil {
		return time.Time{}, err
	}
	if st.state == 'Z' {
		return time.Time{}, os.ErrNotExist
	}
	boot, err := bootTime()
	if err != nil {
		return time.Time{}, err
	}
	return boot.Add(time.Duration(st.ticks) * time.Second / clockTicks), nil
}

// groupRunning reports whether any member of the process group is not a
// zombie. A pid that exits while the listing is read is skipped; a listing
// that cannot be read is an error, which is not absence.
func groupRunning(pgid int) (bool, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		st, err := readProcStat(pid)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
				continue
			}
			return false, err
		}
		if st.pgrp == pgid && st.state != 'Z' {
			return true, nil
		}
	}
	return false, nil
}

func bootTime() (time.Time, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return time.Time{}, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if rest, ok := strings.CutPrefix(scanner.Text(), "btime "); ok {
			secs, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
			if err != nil {
				return time.Time{}, err
			}
			return time.Unix(secs, 0), nil
		}
	}
	return time.Time{}, errors.New("driver: no btime in /proc/stat")
}
