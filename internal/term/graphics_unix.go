//go:build unix

package term

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// readKittyReply reads with select(2) rather than SetReadDeadline: macOS tty
// files report "file type does not support deadline", so a deadline cannot be
// used for the capability probe on the pane's real stdin.
func readKittyReply(in *os.File, total time.Duration) []byte {
	fd := int(in.Fd())
	// select's fd_set has FD_SETSIZE entries. A pane's stdin is normally 0;
	// a caller above the limit gets no probe rather than a blocked read.
	if fd < 0 || fd >= 1024 {
		return nil
	}
	deadline := time.Now().Add(total)
	buf := make([]byte, 256)
	var data []byte
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		var set unix.FdSet
		set.Set(fd)
		tv := unix.NsecToTimeval(remaining.Nanoseconds())
		n, err := unix.Select(fd+1, &set, nil, nil, &tv)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			break
		}
		if n == 0 {
			break // the terminal stayed silent: no graphics support
		}
		n, err = unix.Read(fd, buf)
		if n > 0 {
			data = append(data, buf[:n]...)
			if len(data) > 4096 || ParseKittyGraphicsReply(string(data)) {
				break
			}
			deadline = time.Now().Add(60 * time.Millisecond)
			continue
		}
		if err != nil && err != unix.EINTR && err != unix.EAGAIN {
			break
		}
	}
	return data
}
