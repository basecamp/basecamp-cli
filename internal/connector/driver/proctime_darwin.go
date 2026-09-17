package driver

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// processStartTime is when the kernel started pid, from kern.proc.pid.
func processStartTime(pid int) (time.Time, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		// kern.proc.pid answers a pid with no process with EIO or ESRCH,
		// not an empty record: that is a process that is gone.
		if errors.Is(err, unix.EIO) || errors.Is(err, unix.ESRCH) {
			return time.Time{}, os.ErrNotExist
		}
		return time.Time{}, err
	}
	if info.Proc.P_pid != int32(pid) {
		return time.Time{}, os.ErrNotExist
	}
	tv := info.Proc.P_starttime
	return time.Unix(int64(tv.Sec), int64(tv.Usec)*1000), nil
}
