package term

import (
	"os"
	"regexp"
	"strings"
	"time"
)

// kittyGraphicsQuery is the documented kitty graphics capability probe. The
// image id is echoed in the answer; a=q never displays the payload.
const kittyGraphicsQuery = "\x1b_Gi=31,s=1,v=1,a=q,t=d,f=24;AAAA\x1b\\"

// QueryKittyGraphics asks the terminal that draws this pane whether it accepts
// kitty graphics. It is a query, not an environment guess: a multiplexer can
// answer for the terminal actually attached to it, even when TERM names a plain
// xterm. Silence is a no; a late answer is ignored by ReadEvent as an APC.
func QueryKittyGraphics(in *os.File, out interface{ Write([]byte) (int, error) }) bool {
	if in == nil || out == nil || strings.EqualFold(os.Getenv("TERM_GRAPHICS"), "none") {
		return false
	}
	if _, err := out.Write([]byte(kittyGraphicsQuery)); err != nil {
		return false
	}
	data := readKittyReply(in, 350*time.Millisecond)
	return ParseKittyGraphicsReply(string(data))
}

var kittyReplyRE = regexp.MustCompile(`\x1b_G([^\x1b]*)\x1b\\`)

// ParseKittyGraphicsReply reports whether s contains an OK for the probe.
// Errors, including a file-medium refusal, are not support.
func ParseKittyGraphicsReply(s string) bool {
	for _, m := range kittyReplyRE.FindAllStringSubmatch(s, -1) {
		payload := m[1]
		if i := strings.LastIndexByte(payload, ';'); i >= 0 {
			payload = payload[i+1:]
		}
		if strings.TrimSpace(payload) == "OK" {
			return true
		}
	}
	return false
}
