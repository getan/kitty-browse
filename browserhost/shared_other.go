//go:build !darwin && !linux

package browserhost

import (
	"errors"
	"os"
)

func sharedOwned(os.FileInfo) bool { return false }
func sharedLock(string) (func(), error) {
	return nil, errors.New("shared browser sockets require macOS or Linux")
}
