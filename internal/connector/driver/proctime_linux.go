package driver

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// clockTicks is USER_HZ, which Linux fixes at 100 for /proc on every
// architecture Go releases for.
const clockTicks = 100

// processStartTime is when the kernel started pid: /proc/<pid>/stat's
// starttime, in ticks since boot, plus the boot time from /proc/stat.
func processStartTime(pid int) (time.Time, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return time.Time{}, err
	}
	// The command name is parenthesized and may hold spaces or parentheses;
	// the fields after the last ')' are fixed.
	end := strings.LastIndexByte(string(raw), ')')
	if end < 0 {
		return time.Time{}, errors.New("driver: unreadable /proc stat")
	}
	fields := strings.Fields(string(raw)[end+1:])
	// Field 22 of the line is index 19 after the state (field 3).
	if len(fields) < 20 {
		return time.Time{}, errors.New("driver: short /proc stat")
	}
	ticks, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("driver: /proc stat starttime: %w", err)
	}
	boot, err := bootTime()
	if err != nil {
		return time.Time{}, err
	}
	return boot.Add(time.Duration(ticks) * time.Second / clockTicks), nil
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
