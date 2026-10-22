package browser

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
)

const identityTimeout = 5 * time.Second

// IdentityOptions controls the per-target browser identity override. Stealth is
// deliberately narrow: it removes Chromium's simple headless user-agent marker
// while retaining the browser's real version and platform. It is not a promise
// to bypass bot detection, challenges, or other site security controls.
type IdentityOptions struct {
	Stealth   bool
	UserAgent string
}

type identityOptionsKey struct{}

// withIdentityOptions stores a value (rather than a pointer) so all tab
// contexts derived from the allocator inherit a stable set of options.
func withIdentityOptions(parent context.Context, opts IdentityOptions) context.Context {
	return context.WithValue(parent, identityOptionsKey{}, opts)
}

// applyIdentity applies identity options to the current chromedp target. It is
// intended to run immediately after the target's initial chromedp.Do and before
// its first navigation. Empty options are a true no-op and perform no CDP calls.
func applyIdentity(ctx context.Context) error {
	opts, _ := ctx.Value(identityOptionsKey{}).(IdentityOptions)
	if !opts.Stealth && opts.UserAgent == "" {
		return nil
	}

	applyCtx, cancel := context.WithTimeout(ctx, identityTimeout)
	defer cancel()

	// A caller-supplied UA is authoritative. In particular, do not attach real
	// Chromium Client Hint metadata to an arbitrary string: its platform and
	// brands may be unrelated. Omitting metadata also prevents CDP from inventing
	// architecture or other high-entropy values on our behalf.
	if opts.UserAgent != "" {
		if _, err := chromedp.Call(applyCtx, emulation.SetUserAgentOverride,
			emulation.SetUserAgentOverrideParams{UserAgent: opts.UserAgent}); err != nil {
			return fmt.Errorf("apply custom user agent: %w", err)
		}
		return nil
	}

	version, err := chromedp.Call(applyCtx, browser.GetVersion, cdp.Empty{})
	if err != nil {
		return fmt.Errorf("read Chromium user agent: %w", err)
	}

	metadata := readUserAgentMetadata(applyCtx)
	params := emulation.SetUserAgentOverrideParams{
		UserAgent:         pureUA(version.UserAgent),
		UserAgentMetadata: metadata,
	}
	if _, err := chromedp.Call(applyCtx, emulation.SetUserAgentOverride, params); err != nil {
		return fmt.Errorf("apply stealth user agent: %w", err)
	}
	return nil
}

// pureUA makes only Chromium's documented headless token look like the normal
// desktop token. It does not replace version numbers, platform text, or any
// other part of the user agent.
func pureUA(userAgent string) string {
	return strings.ReplaceAll(userAgent, "HeadlessChrome/", "Chrome/")
}

type userAgentMetadataValue struct {
	Brands          []*emulation.UserAgentBrandVersion `json:"brands"`
	FullVersionList []*emulation.UserAgentBrandVersion `json:"fullVersionList"`
	Platform        string                             `json:"platform"`
	PlatformVersion string                             `json:"platformVersion"`
	Architecture    string                             `json:"architecture"`
	Model           string                             `json:"model"`
	Mobile          bool                               `json:"mobile"`
	Bitness         string                             `json:"bitness"`
	Wow64           bool                               `json:"wow64"`
	FormFactors     []string                           `json:"formFactors"`
}

const readUserAgentMetadataJS = `(async () => {
  const data = navigator.userAgentData;
  if (!data || typeof data.getHighEntropyValues !== "function") return null;
  try {
    const value = await data.getHighEntropyValues([
      "architecture", "bitness", "formFactors", "fullVersionList",
      "model", "platformVersion", "wow64"
    ]);
    return {
      brands: value.brands,
      fullVersionList: value.fullVersionList,
      platform: value.platform,
      platformVersion: value.platformVersion,
      architecture: value.architecture,
      model: value.model,
      mobile: value.mobile,
      bitness: value.bitness,
      wow64: value.wow64,
      formFactors: value.formFactors
    };
  } catch (_) {
    return null;
  }
})()`

// readUserAgentMetadata is best-effort. Some Chromium versions or initial
// about:blank targets do not expose UA Client Hints. In that case a UA-only
// override is safer than fabricating platform or hardware fields.
func readUserAgentMetadata(ctx context.Context) *emulation.UserAgentMetadata {
	value, err := chromedp.Run(ctx, chromedp.Evaluate[*userAgentMetadataValue](readUserAgentMetadataJS))
	if err != nil || value == nil {
		return nil
	}
	return &emulation.UserAgentMetadata{
		Brands:          desktopBrands(value.Brands),
		FullVersionList: desktopBrands(value.FullVersionList),
		Platform:        value.Platform,
		PlatformVersion: value.PlatformVersion,
		Architecture:    value.Architecture,
		Model:           value.Model,
		Mobile:          value.Mobile,
		Bitness:         value.Bitness,
		Wow64:           value.Wow64,
		FormFactors:     value.FormFactors,
	}
}

// Keep real brands/versions, but don't leave a HeadlessChrome brand beside the
// normalized UA. Prefer the native Chromium entry if both are already present.
func desktopBrands(native []*emulation.UserAgentBrandVersion) []*emulation.UserAgentBrandVersion {
	var out []*emulation.UserAgentBrandVersion
	seen := make(map[emulation.UserAgentBrandVersion]bool)
	for _, value := range native {
		if value == nil {
			continue
		}
		brand := *value
		if brand.Brand == "HeadlessChrome" {
			brand.Brand = "Chromium"
		}
		if !seen[brand] {
			seen[brand] = true
			out = append(out, &brand)
		}
	}
	return out
}
