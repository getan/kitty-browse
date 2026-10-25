// Command kitty-browse draws a real Chromium page in a terminal that supports
// the Kitty graphics protocol (or iTerm2/Sixel through rasterm).
//
// The whole implementation is the public browsepane package. This binary owns
// only three things a library must not: the build metadata injected at release
// time, the interrupt handlers, and the exit status. golder serves the same pane
// from its own binary through that package.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"

	"github.com/getan/kitty-browse/browsepane"
)

func main() {
	if handleVersion(os.Args[1:]) {
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer stop()
	err := browsepane.Run(ctx, browsepane.Options{
		Args:    os.Args[1:],
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Name:    os.Args[0],
		Version: versionLine(),
	})
	// browsepane.Run has already released Chromium, the renderer and the control
	// socket; -h is a request for help, not a failure.
	if err != nil && !errors.Is(err, flag.ErrHelp) {
		log.Fatal(err)
	}
}
