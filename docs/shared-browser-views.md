# Shared profile, independent conversation views

`browserhost.Hub` owns one Chromium allocator and persistent profile. `OpenView`
returns a regular Host, but its tab set reconciles only its own pages and their
popups. Every view owns a browser identity, automation socket, selected tab,
viewport state, interaction/pause state and shutdown. Cookie and origin storage
remain Chromium-managed and shared. No cookie copying or account migration is
performed.

`ServeShared` supplies the cross-process owner. Embedders derive its private
state and short runtime directories from a dedicated profile. One advisory lock
arbitrates concurrent owner startup. A bounded management API authenticates
frontend launches using an owner-only credential in private state storage.
Clients derive view IDs from trusted conversation IDs, never model input.

Opening the same view is idempotent and does not navigate it. `/reconnect` only
restores a live operator connection and does not create a view. Independent
instances use different instance names. A view's quit never kills another view.
The last view's closure starts an idle grace period (30 seconds by default),
after which the owner flushes and closes the profile.

## Automation versus operator input

The automation socket retains the existing `/v1/info`, `/v1/actions`, screenshot
and quit API. It has no operator-input endpoint. A separate per-view capability
on the private management connection authenticates manual input and a closed
set of browser chrome operations. Capabilities cannot access another view and
must not be exposed in model output, arguments or logs. The embedding runtime
must keep the master credential outside its agents' file boundary.

`RemoteView.SendOperatorInput` preserves the existing ManualTarget exclusion;
`OperatorAction` accepts only native navigation, tab/viewport changes, pause,
explicit resume and view quit. It never accepts eval, cookie reads, upload or
arbitrary page operations. Operator chrome may work while automation is paused,
but leaves that pause in place. Only explicit resume clears it.

`browsepane.RunSharedPane` is a terminal operator surface for a RemoteView,
sharing the existing terminal parsers, toolbar and image encoders. It forwards
ordinary typing (including q/r/T/W) to the page; Ctrl+L edits the address,
Ctrl+G explicitly resumes, and Ctrl+Q closes only that view. The shared pane
uses bounded PNG frames rather than owning Chromium's screencast connection.
The standalone `browsepane.Run` path is unchanged.

## Validation

Unit/race coverage exercises scoped popup ownership, concurrent view startup,
identity/socket conflicts, partial-startup cleanup, operator capability isolation
and terminal input mapping. `KITTY_BROWSE_TEST_HUB=1` enables the private Chromium
integration test. Real Chromium end-to-end testing of the embedding GUI/TUI is
separate and is not implied by the browser-free suite.
