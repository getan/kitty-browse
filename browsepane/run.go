package browsepane

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// Build metadata for embedders. The kitty-browse binary injects its own values
// at release time and passes them through Options.Version; these defaults only
// keep "-version" answerable when a host application omits that field.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// VersionLine is the one line every caller parses. Keep the format stable:
// "kitty-browse <version> (commit <sha>, built <time>)".
func VersionLine() string {
	return fmt.Sprintf("kitty-browse %s (commit %s, built %s)", Version, Commit, Date)
}

// Options describes one pane run. The zero value is usable: it reads and draws
// on the standard streams exactly like the kitty-browse binary.
type Options struct {
	// Args is the argument vector after the program name: the same flags, the
	// same subcommands (action, info, screenshot, version) and the same single
	// optional URL the kitty-browse binary accepts.
	Args []string
	// Stdin is the terminal the pane reads and draws into. It must be a real
	// terminal for the visible pane; nil means os.Stdin.
	Stdin *os.File
	// Stdout receives the pane's escape sequences, and the client subcommands'
	// documents. nil means os.Stdout.
	Stdout io.Writer
	// Stderr receives usage text and diagnostics. nil means os.Stderr.
	Stderr io.Writer
	// StartupOutput is an optional startup-only Chromium diagnostics sink.
	// Embedders must bound/redact it. It is disabled once CDP has connected,
	// before page interaction or terminal input starts; private mode ignores it.
	StartupOutput io.Writer
	// StartupPhase observes coarse startup milestones, never URLs or input.
	StartupPhase func(string)
	// RequireChromiumSandbox prevents the allocator's automatic --no-sandbox
	// fallback for root. A controlled host must fail rather than disable it.
	RequireChromiumSandbox bool
	// Name is the program name usage output shows. Empty means "kitty-browse".
	Name string
	// Version is the one-line build description answered by -version. Empty
	// means VersionLine().
	Version string
	// Resize optionally supplies terminal resize notifications. When it is nil
	// Run subscribes to SIGWINCH on Unix for the duration of the run and stops
	// the subscription before returning; on platforms without that signal the
	// pane simply never sees a resize event.
	Resize <-chan os.Signal
}

func (o *Options) normalize() {
	if o.Stdin == nil {
		o.Stdin = os.Stdin
	}
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	if o.Name == "" {
		o.Name = "kitty-browse"
	}
	if o.Version == "" {
		o.Version = VersionLine()
	}
}

// Run serves one browser: the visible pane, or one of the client subcommands
// that talk to an already running pane over its control socket.
//
// It returns when the pane quits, when the control API asks it to quit, when
// ctx is canceled, or on the first error. Run never calls os.Exit and never
// touches flag.CommandLine, so a host application (golder's hidden
// "golder browse ...") can call it directly and own the exit status. All the
// browser, renderer and control-socket state is released before Run returns.
func Run(ctx context.Context, opts Options) error {
	if ctx == nil {
		ctx = context.Background()
	}
	opts.normalize()
	if len(opts.Args) == 0 {
		return runPane(ctx, opts)
	}
	var err error
	switch strings.TrimSpace(opts.Args[0]) {
	case "-version", "--version", "version":
		_, err = fmt.Fprintln(opts.Stdout, opts.Version)
		return err
	case "action":
		err = runAction(opts.Args[1:], opts.Stdin, opts.Stdout, opts.Stderr)
	case "info":
		err = runInfo(opts.Args[1:], opts.Stdout, opts.Stderr)
	case "screenshot":
		err = runScreenshot(opts.Args[1:], opts.Stdout, opts.Stderr)
	default:
		return runPane(ctx, opts)
	}
	return helpAsSuccess(err)
}

// helpAsSuccess keeps "-h" a success for the client subcommands: flag already
// printed the usage text, and asking for help is not a failure.
func helpAsSuccess(err error) error {
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	return err
}
