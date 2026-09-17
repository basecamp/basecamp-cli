package driver

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// processStartTime is when the kernel started pid, from kern.proc.pid.
func processStartTime(pid int) (time.Time, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return time.Time{}, err
	}
	if info.Proc.P_pid != int32(pid) {
		return time.Time{}, os.ErrNotExist
	}
	tv := info.Proc.P_starttime
	return time.Unix(int64(tv.Sec), int64(tv.Usec)*1000), nil
}
