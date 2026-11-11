# Checked browser recovery

An automation pause is a latch, not a transfer of ownership. Normal login forms,
redirects and page loading do not by themselves require a pause. Recognized
visible verification, document HTTP 429 and explicit intervention still block
new automatic mutations. Manual input and read-only status remain available.

## Public observation, private decisions

`interaction.status.v2` advertises the additive recovery contract in `/v1/info`.
The existing `interaction.status` also returns its non-sensitive fields:
`browserId`, `instanceEpoch`, `pauseGeneration`, `pauseSource`,
`resumedGeneration`, `continuationVersion=1`, `closed` and optional `retryAfterMs`.
The instance epoch changes when the view is recreated. Each new pause advances
its generation; a queued mutation from an older generation stays rejected even
after recovery.

The public `interaction.resume` name remains for diagnostics, but cannot clear a
pause. Neither a model-supplied flag nor possession of a request ID is consent.
`Host.ContinueChecked` is an in-process operator seam. `RemoteView.ContinueChecked`
uses the authenticated, exact per-view operator connection and a dedicated
management endpoint. The master broker key, another view's key and the ordinary
automation socket cannot submit that decision.

The request binds BrowserID, instance epoch, pause generation, a random request
ID and an expiry no more than five minutes away. The host waits at most five
seconds for mutating automatic/operator operations to drain, then compares and
clears the latch under its interaction lock. It sends no page input and never
replays a prior action. A new pause, closure or changed instance invalidates an
old decision. A server's explicit Retry-After deadline is retained and prevents
recovery/input until it expires; recovery does not disable verification checks.

## Receipts, not transport retries

Each view retains at most 64 receipts for ten minutes. A repeated request ID
returns its recorded result without applying another recovery, including when
the view has since paused again. `ContinuationOutcome` reports `pending`,
`not_consumed`, `resumed`, `stale` or `unknown`. `not_consumed` is a completed
negative acknowledgement, not an assumption based on a still-paused page.
An uncertain transport result is queried rather than resubmitted. Receipt
eviction or restart is not evidence that replay is safe. Closing a view revokes
pending decisions and removes its receipts.

`RemoteView.Attest` returns a fresh-challenge-bound proof over that private
connection: view/browser/epoch/socket, login identity, proxy mode, Chromium
sandbox requirement and a digest of the immutable runtime configuration. These
are runtime facts, not an embedding application's permission grant. In
particular, `browser_runtime` never claims an outer OS filesystem sandbox.
Operator credentials and profile contents are not returned to automation.

## Operator surfaces

Both shared and standalone browser panes show clickable recovery buttons in the
paused footer. A pointer press is bound to its button, dimensions, epoch and
generation; a changed target or resize cancels it. Footer events never reach the
webpage. Tab/Enter/Space and auxiliary Ctrl+G use the same checked operation.
Submitting, failed and uncertain recovery have visible status; unknown outcomes
do not enable another recovery submission.

Embedders should subscribe to bounded lightweight status, remove the old
request after the exact resumed generation is observed, and re-observe the page
before continuing a task. Browser recovery and task scheduling are separate:
cancelled, replaced or read-only conversations must not be restarted.

## Validation

The local suite covers CAS generations, expiry, draining and cancellation,
receipt bounds, closed views, cross-view credentials, attestation challenges,
button pointer/keyboard events and resize. Installed-Chromium localhost tests
verify that a visible challenge is re-latched and a document 429 Retry-After
cannot be cleared by explicit recovery. No real website account is used.
