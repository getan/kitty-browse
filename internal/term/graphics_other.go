//go:build !unix

package term

import (
	"os"
	"time"
)

// readKittyReply is the non-unix fallback. A console that cannot set a read
// deadline answers no rather than blocking the pane's startup.
func readKittyReply(in *os.File, total time.Duration) []byte {
	if err := in.SetReadDeadline(time.Now().Add(150 * time.Millisecond)); err != nil {
		return nil
	}
	buf := make([]byte, 256)
	var data []byte
	deadline := time.Now().Add(total)
	for {
		_ = in.SetReadDeadline(deadline)
		n, err := in.Read(buf)
		if n > 0 {
			data = append(data, buf[:n]...)
			if len(data) > 4096 || ParseKittyGraphicsReply(string(data)) {
				break
			}
			deadline = time.Now().Add(60 * time.Millisecond)
			continue
		}
		if err != nil {
			break
		}
	}
	_ = in.SetReadDeadline(time.Time{})
	return data
}
