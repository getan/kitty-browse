package action

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ParseCommand converts the documented CLI vocabulary to the stable API
// request. It never provides a catch-all passthrough.
func ParseCommand(args []string) (Request, error) {
	var req Request
	if len(args) == 0 {
		return req, invalid("missing action")
	}
	req.Args = append([]string(nil), args[1:]...)
	cmd, rest := strings.ToLower(args[0]), args[1:]
	p := map[string]any{}
	require := func(n int, usage string) error {
		if len(rest) < n {
			return invalid("usage: " + usage)
		}
		return nil
	}
	noExtra := func(n int, usage string) error {
		if len(rest) != n {
			return invalid("usage: " + usage)
		}
		return nil
	}

	switch cmd {
	case "snapshot":
		req.Action = "snapshot"
		for len(rest) > 0 {
			switch rest[0] {
			case "-i", "--interactive":
				p["interactive"] = true
				rest = rest[1:]
			case "-c", "--compact":
				p["compact"] = true
				rest = rest[1:]
			case "-d", "--depth":
				if len(rest) < 2 {
					return req, invalid("snapshot depth requires a value")
				}
				n, err := positiveInt(rest[1], true)
				if err != nil {
					return req, invalid("snapshot depth must be a non-negative integer")
				}
				p["depth"] = n
				rest = rest[2:]
			default:
				return req, invalid("unknown snapshot option: " + rest[0])
			}
		}
	case "eval":
		if err := require(1, "eval JS"); err != nil {
			return req, err
		}
		req.Action = "eval"
		p["script"] = strings.Join(rest, " ")
	case "click", "dblclick", "hover", "focus", "check", "uncheck":
		if err := noExtra(1, cmd+" locator"); err != nil {
			return req, err
		}
		req.Action = cmd
		setLocator(p, rest[0])
	case "fill", "type":
		if err := noExtra(2, cmd+" locator value"); err != nil {
			return req, err
		}
		req.Action = cmd
		setLocator(p, rest[0])
		p["text"] = rest[1]
	case "press", "keydown", "keyup":
		if err := noExtra(1, cmd+" key"); err != nil {
			return req, err
		}
		req.Action = cmd
		p["key"] = rest[0]
	case "keyboard":
		if len(rest) == 0 || strings.ToLower(rest[0]) != "insert" {
			return req, invalid("usage: keyboard insert TEXT")
		}
		rest = rest[1:]
		if err := noExtra(1, "keyboard insert TEXT"); err != nil {
			return req, err
		}
		req.Action = "keyboard.insert"
		p["text"] = rest[0]
	case "select":
		if err := noExtra(2, "select locator value"); err != nil {
			return req, err
		}
		req.Action = cmd
		setLocator(p, rest[0])
		p["value"] = rest[1]
	case "upload":
		if err := require(2, "upload locator file [file ...]"); err != nil {
			return req, err
		}
		req.Action = cmd
		setLocator(p, rest[0])
		p["files"] = rest[1:]
	case "get":
		if err := require(1, "get kind [locator] [name]"); err != nil {
			return req, err
		}
		if len(rest) > 3 {
			return req, invalid("usage: get kind [locator] [name]")
		}
		req.Action = "get"
		p["kind"] = rest[0]
		if len(rest) >= 2 {
			setLocator(p, rest[1])
		}
		if len(rest) == 3 {
			if strings.ToLower(rest[0]) != "attr" {
				return req, invalid("only get attr accepts a name")
			}
			p["name"] = rest[2]
		}
	case "is":
		if err := noExtra(2, "is visible|enabled|checked locator"); err != nil {
			return req, err
		}
		req.Action, p["kind"] = cmd, rest[0]
		setLocator(p, rest[1])
	case "find":
		if len(rest) < 2 {
			return req, invalid("usage: find role|text|name value [--exact]")
		}
		req.Action = cmd
		kind := strings.ToLower(rest[0])
		if kind != "role" && kind != "text" && kind != "name" {
			return req, invalid("find kind must be role, text, or name")
		}
		p[kind] = rest[1]
		rest = rest[2:]
		for len(rest) > 0 {
			switch rest[0] {
			case "--exact":
				p["exact"] = true
				rest = rest[1:]
			case "--name", "--text":
				if len(rest) < 2 {
					return req, invalid(rest[0] + " requires a value")
				}
				p[strings.TrimPrefix(rest[0], "--")] = rest[1]
				rest = rest[2:]
			default:
				return req, invalid("unknown find option: " + rest[0])
			}
		}
	case "scrollintoview":
		if err := noExtra(1, "scrollintoview locator"); err != nil {
			return req, err
		}
		req.Action = cmd
		setLocator(p, rest[0])
	case "wait":
		if len(rest) > 0 && (strings.EqualFold(rest[0], "url") || strings.EqualFold(rest[0], "function") || strings.EqualFold(rest[0], "loadstate") || strings.EqualFold(rest[0], "popup")) {
			sub := strings.ToLower(rest[0])
			rest = rest[1:]
			if len(rest) == 0 && sub != "popup" {
				return req, invalid("usage: wait " + sub + " value [--exact] [--timeout-ms N]")
			}
			value := ""
			if len(rest) > 0 && !strings.HasPrefix(rest[0], "--") {
				value = rest[0]
				rest = rest[1:]
			}
			if value == "" && sub != "popup" {
				return req, invalid("usage: wait " + sub + " value [--exact] [--timeout-ms N]")
			}
			switch sub {
			case "url":
				req.Action, p["pattern"] = "wait.url", value
			case "function":
				req.Action, p["script"] = "wait.function", value
			case "loadstate":
				req.Action, p["state"] = "wait.loadState", strings.ToLower(value)
			case "popup":
				req.Action = "wait.popup"
				if value != "" {
					p["urlPattern"] = value
				}
			}
			for len(rest) > 0 {
				if rest[0] == "--exact" && sub == "url" {
					p["exact"] = true
					rest = rest[1:]
					continue
				}
				if rest[0] == "--timeout-ms" && len(rest) >= 2 {
					n, err := positiveInt(rest[1], false)
					if err != nil || n > 120000 {
						return req, invalid("timeoutMs must be between 1 and 120000")
					}
					req.TimeoutMS = n
					rest = rest[2:]
					continue
				}
				return req, invalid("unknown wait option: " + rest[0])
			}
		} else {
			if err := noExtra(1, "wait locator|milliseconds"); err != nil {
				return req, err
			}
			req.Action = cmd
			if n, err := strconv.Atoi(rest[0]); err == nil && n >= 0 {
				p["timeoutMs"] = n
			} else {
				setLocator(p, rest[0])
			}
		}
	case "dialog":
		if len(rest) == 0 {
			return req, invalid("usage: dialog status|accept|dismiss [promptText]")
		}
		sub := strings.ToLower(rest[0])
		rest = rest[1:]
		switch sub {
		case "status", "dismiss":
			if err := noExtra(0, "dialog "+sub); err != nil {
				return req, err
			}
			req.Action = "dialog." + sub
		case "accept":
			if len(rest) > 1 {
				return req, invalid("usage: dialog accept [promptText]")
			}
			req.Action = "dialog.accept"
			if len(rest) == 1 {
				p["promptText"] = rest[0]
				// Args may be included in diagnostic envelopes. Keep the value only
				// in the action parameters that must be sent to CDP.
				req.Args = []string{"accept", "[REDACTED]"}
			}
		default:
			return req, unsupported("dialog." + sub)
		}
	case "scroll":
		if len(rest) == 0 || len(rest) > 2 {
			return req, invalid("usage: scroll up|down|left|right [amount]")
		}
		req.Action = cmd
		amount := 500.0
		if len(rest) == 2 {
			n, err := strconv.ParseFloat(rest[1], 64)
			if err != nil || n < 0 {
				return req, invalid("scroll amount must be a non-negative number")
			}
			amount = n
		}
		switch strings.ToLower(rest[0]) {
		case "up":
			p["deltaY"] = -amount
		case "down":
			p["deltaY"] = amount
		case "left":
			p["deltaX"] = -amount
		case "right":
			p["deltaX"] = amount
		default:
			return req, invalid("scroll direction must be up, down, left, or right")
		}
	case "frame":
		if err := noExtra(1, "frame locator|main"); err != nil {
			return req, err
		}
		req.Action = cmd
		if rest[0] == "main" {
			p["main"] = true
		} else {
			setLocator(p, rest[0])
		}
	case "open":
		if err := noExtra(1, "open URL"); err != nil {
			return req, err
		}
		req.Action = "tab.new"
		p["url"] = rest[0]
	case "goto", "navigate":
		if err := noExtra(1, cmd+" URL"); err != nil {
			return req, err
		}
		req.Action = "navigate"
		p["url"] = rest[0]
	case "back", "forward", "reload", "info", "capabilities", "done", "quit":
		if err := noExtra(0, cmd); err != nil {
			return req, err
		}
		req.Action = cmd
	case "console", "errors":
		req.Action = cmd
		if len(rest) == 1 && rest[0] == "--clear" {
			p["clear"] = true
		} else if len(rest) != 0 {
			return req, invalid("usage: " + cmd + " [--clear]")
		}
	case "set":
		if len(rest) != 3 || rest[0] != "viewport" {
			return req, invalid("usage: set viewport width height")
		}
		w, e1 := positiveInt(rest[1], false)
		h, e2 := positiveInt(rest[2], false)
		if e1 != nil || e2 != nil {
			return req, invalid("viewport dimensions must be positive integers")
		}
		req.Action = "set.viewport"
		p["width"], p["height"] = w, h
	case "tab":
		if len(rest) == 0 {
			return req, invalid("usage: tab list|new|close|activate")
		}
		sub := strings.ToLower(rest[0])
		rest = rest[1:]
		switch sub {
		case "list":
			if err := noExtra(0, "tab list"); err != nil {
				return req, err
			}
			req.Action = "tab.list"
		case "new":
			if len(rest) > 1 {
				return req, invalid("usage: tab new [URL]")
			}
			req.Action = "tab.new"
			if len(rest) == 1 {
				p["url"] = rest[0]
			}
		case "close", "activate":
			if len(rest) > 1 {
				return req, invalid("usage: tab " + sub + " [tabId]")
			}
			req.Action = "tab." + sub
			if len(rest) == 1 {
				p["tabId"] = rest[0]
			}
		default:
			return req, unsupported("tab." + sub)
		}
	case "network":
		if len(rest) == 0 {
			return req, invalid("usage: network requests|response")
		}
		sub := strings.ToLower(rest[0])
		rest = rest[1:]
		switch sub {
		case "requests":
			req.Action = "network.requests"
			for len(rest) > 0 {
				switch rest[0] {
				case "--clear":
					p["clear"] = true
					rest = rest[1:]
				case "--filter":
					if len(rest) < 2 {
						return req, invalid("--filter requires a value")
					}
					p["filter"] = rest[1]
					rest = rest[2:]
				default:
					return req, invalid("unknown network requests option: " + rest[0])
				}
			}
		case "response":
			if err := noExtra(1, "network response requestId"); err != nil {
				return req, err
			}
			req.Action = "network.response"
			p["requestId"] = rest[0]
		default:
			return req, unsupported("network." + sub)
		}
	case "download":
		if len(rest) == 0 {
			return req, invalid("usage: download wait [guid] [--timeout-ms N]|list")
		}
		sub := strings.ToLower(rest[0])
		rest = rest[1:]
		switch sub {
		case "list":
			if err := noExtra(0, "download list"); err != nil {
				return req, err
			}
			req.Action = "download.list"
		case "wait":
			req.Action = "download.wait"
			if len(rest) > 0 && rest[0] != "--timeout-ms" {
				p["guid"] = rest[0]
				rest = rest[1:]
			}
			if len(rest) > 0 {
				if len(rest) != 2 || rest[0] != "--timeout-ms" {
					return req, invalid("usage: download wait [guid] [--timeout-ms N]")
				}
				n, err := positiveInt(rest[1], false)
				if err != nil || n > 120000 {
					return req, invalid("timeoutMs must be between 1 and 120000")
				}
				req.TimeoutMS = n
			}
		default:
			return req, unsupported("download." + sub)
		}
	case "cookies":
		if len(rest) == 0 {
			return req, invalid("usage: cookies get|set|clear")
		}
		sub := strings.ToLower(rest[0])
		rest = rest[1:]
		req.Action = "cookies." + sub
		switch sub {
		case "get":
			if len(rest) > 1 {
				return req, invalid("usage: cookies get [name]")
			}
			if len(rest) == 1 {
				p["name"] = rest[0]
			}
		case "set":
			if len(rest) < 2 {
				return req, invalid("usage: cookies set name value [options]")
			}
			p["name"], p["value"] = rest[0], rest[1]
			if err := parseCookieOptions(rest[2:], p); err != nil {
				return req, err
			}
		case "clear":
			if len(rest) > 1 {
				return req, invalid("usage: cookies clear [name]")
			}
			if len(rest) == 1 {
				p["name"] = rest[0]
			}
		default:
			return req, unsupported(req.Action)
		}
	case "storage":
		if len(rest) < 2 {
			return req, invalid("usage: storage local|session get|set|clear")
		}
		area, sub := strings.ToLower(rest[0]), strings.ToLower(rest[1])
		if area != "local" && area != "session" {
			return req, invalid("storage area must be local or session")
		}
		rest = rest[2:]
		req.Action = "storage." + sub
		p["kind"] = area
		switch sub {
		case "get":
			if len(rest) > 1 {
				return req, invalid("usage: storage " + area + " get [key]")
			}
			if len(rest) == 1 {
				p["key"] = rest[0]
			}
		case "set":
			if len(rest) != 2 {
				return req, invalid("usage: storage " + area + " set key value")
			}
			p["key"], p["value"] = rest[0], rest[1]
		case "clear":
			if len(rest) != 0 {
				return req, invalid("usage: storage " + area + " clear")
			}
		default:
			return req, unsupported(req.Action)
		}
	case "screenshot":
		req.Action = cmd
		for len(rest) > 0 {
			if rest[0] == "--full" {
				p["full"] = true
				rest = rest[1:]
				continue
			}
			if strings.HasPrefix(rest[0], "-") {
				return req, invalid("unknown screenshot option: " + rest[0])
			}
			if _, ok := p["path"]; ok {
				return req, invalid("usage: screenshot [file] [--full]")
			}
			p["path"] = rest[0]
			rest = rest[1:]
		}
	case "pdf":
		if len(rest) > 1 {
			return req, invalid("usage: pdf [file]")
		}
		req.Action = cmd
		if len(rest) == 1 {
			p["path"] = rest[0]
		}
	default:
		return req, unsupported(cmd)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return req, fmt.Errorf("encode parameters: %w", err)
	}
	req.Params = raw
	return req, nil
}

func parseCookieOptions(args []string, p map[string]any) error {
	for len(args) > 0 {
		name := strings.TrimPrefix(args[0], "--")
		if name == args[0] {
			return invalid("cookie options must begin with --")
		}
		switch name {
		case "http-only":
			name = "httpOnly"
		case "same-site":
			name = "sameSite"
		}
		if name == "secure" || name == "httpOnly" {
			p[name] = true
			args = args[1:]
			continue
		}
		if name != "url" && name != "path" && name != "domain" && name != "expires" && name != "sameSite" {
			return invalid("unknown cookie option: " + args[0])
		}
		if len(args) < 2 {
			return invalid("cookie option --" + name + " requires a value")
		}
		if name == "expires" {
			n, err := strconv.ParseFloat(args[1], 64)
			if err != nil {
				return invalid("cookie expires must be a number")
			}
			p[name] = n
		} else {
			p[name] = args[1]
		}
		args = args[2:]
	}
	return nil
}
func setLocator(p map[string]any, locator string) {
	if strings.HasPrefix(locator, "@") {
		p["ref"] = locator
	} else {
		p["selector"] = locator
	}
}
func positiveInt(s string, zero bool) (int, error) {
	n, e := strconv.Atoi(s)
	if e != nil || n < 0 || (!zero && n == 0) {
		return 0, fmt.Errorf("invalid integer")
	}
	return n, nil
}
func invalid(message string) *Error { return actionError("invalid_arguments", message, false) }
func unsupported(action string) *Error {
	return actionError("unsupported_action", "unsupported action: "+action, false)
}
