# tubctl — improvement tracker

Audit of the codebase as of `661015a` (2026-06-12). Check items off as they land;
add a commit/PR reference next to each completed item.

Baseline at time of audit: `go vet` clean, `go test ./...` all green, build OK.

Legend: 🔴 fix soon · 🟡 worth doing · 🟢 nice to have

---

## 1. Bugs / correctness

- [x] 🔴 **`watch` prints `temp_set` twice and never shows water temperature**
  `cmd/tubctl/watch.go:94-96` — `fmtState` formats `now=%d set=%d` but passes
  `m["temp_set"]` for both. Root cause: `Status.Map()` (`internal/tub/status.go:83`)
  only contains the 14 writable attrs, so `temp_now`, `heat_temp_reach`, and
  `errors` are invisible to `watch` diffs entirely.
  *Fixed: read-only fields added to `Status.Map()` (write paths only look up
  requested keys, so they're inert there); `fmtState` now prints `temp_now`.
  Tests: `TestMapIncludesReadOnlyFields`, `TestFmtStateShowsCurrentAndTargetTemp`.*

- [x] 🟡 **Mixed atomic / non-atomic access to `Client.writeSeq`**
  `internal/tub/client.go` reset `c.writeSeq = 0` with a plain store while
  `Write` used `atomic.AddUint32` — a data race on reconnect with an in-flight
  Write. *Fixed: field is now `atomic.Uint32` (`Store`/`Add`).*

- [ ] 🟡 **Heartbeat failure doesn't mark the client disconnected**
  `internal/tub/client.go:347` — when the ping write fails, `heartbeatLoop` just
  returns; `loggedIn` stays true until the read loop also errors. `LoggedIn()`
  can report a dead session. Call `markDisconnected()` on write failure.

- [x] 🟡 **`Scheduler.Replace` applies schedules in memory even when persisting fails**
  `internal/sched/sched.go` installed the new list, then `save()` failed and the
  HTTP handler returned 400 — the client saw an error but the (unsaved) schedules
  ran until restart. *Fixed: persist first; on save failure the old list stays
  active. `save` now takes the list explicitly.
  Test: `TestReplaceKeepsOldListWhenPersistFails`.*

- [ ] 🟢 **Schedules loaded from disk skip validation and ID reassignment**
  `internal/sched/sched.go:226-239` — a hand-edited `schedules.json` with
  duplicate IDs silently breaks the `inWindow` edge detection. Run entries
  through the same validation/ID-assignment as `Replace`.

- [ ] 🟢 **Ignored error on login write** — `internal/tub/client.go:284`
  `conn.Write(EncodeFrame(CmdLoginReq, …))` error is dropped; a failed login
  write surfaces only as the 8 s login timeout. Log it at least.

- [ ] 🟢 **`EncodeFrame` silently mis-encodes bodies > 0x7fff bytes**
  `internal/tub/frame.go:44-49` — the 2-byte length variant caps at 32 KiB.
  Unreachable with current payloads, but a guard (error or panic) documents the
  limit.

## 2. Security

Context: trusted-LAN design, no TLS, read endpoints intentionally open — the
README documents this honestly. Items below are hardening within that model.

- [x] 🔴 **`clientIP` trusts `X-Forwarded-For` blindly**
  Any client could spoof the IP recorded in the audit log. *Fixed: `clientIP`
  now always uses the socket address (`net.SplitHostPort` on `RemoteAddr`,
  IPv6-safe); XFF is logged as a separate `xff` field, length-capped and
  control-character-stripped. Test: `TestClientIPIgnoresForwardedFor`.*

- [x] 🟡 **Container runs as root**
  *Fixed: runtime stage now has `USER 65534:65534`, and `/data` is baked into
  the image owned by 65534 so fresh named volumes inherit writable ownership
  (verified: `stat` shows `65534:65534`, nobody can write). Caveat: a volume
  created by an older root image keeps root ownership — `chown -R 65534:65534`
  it once when upgrading.*

- [x] 🟡 **No security response headers on the UI**
  *Fixed: `securityHeaders` middleware wraps the whole mux — CSP
  (`default-src 'self'`, `frame-ancestors 'none'`, inline script/style allowed
  for the single-file UI), `X-Content-Type-Options: nosniff`,
  `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`.
  Test: `TestSecurityHeaders`. Follow-up: replace `'unsafe-inline'` with a
  hash/nonce if the UI ever splits into separate files.*

- [ ] 🟡 **No throttling/lockout on auth-token attempts**
  `tokenOK` is constant-time (good) but unlimited; on an exposed deployment the
  token is brute-forceable. A tiny per-IP delay or failure counter on 401s
  would do.

- [ ] 🟢 **SSE slots are consumed by unauthenticated clients**
  `maxSSEClients = 16` (`internal/web/server.go:30`) — anyone on the LAN can pin
  all 16 slots and lock the UI's live updates out. Acceptable on a trusted LAN;
  note it, or scope slots per remote IP.

- [ ] 🟢 **CI: add `gosec` / `staticcheck` and a `-race` test job**
  `.woodpecker.yml` runs govulncheck (deps) but no static analysis of our own
  code, and tests run without the race detector (needs cgo + gcc in the image:
  `golang:alpine` + `apk add build-base`, or a debian-based image).
  *Partially done: the test step now runs a `gofmt -l` gate and `go vet` before
  tests. gosec/staticcheck/-race still open.*

## 3. Code quality / structure

- [x] 🟡 **Five files are not gofmt-clean**
  *Fixed: `gofmt -w .` applied to the whole tree, and the Woodpecker test step
  now fails on `gofmt -l` output (plus runs `go vet`), so it can't regress.*

- [ ] 🟡 **`EncodeControl`'s name-switch duplicates the attribute table**
  `internal/tub/encode.go:139-155` — the `val` closure switches over all 14
  names; adding an attribute means touching three places (table, switch,
  `Status.Map`). Driving everything off `Status.Map()` + the `Writable` table
  would collapse it. Also `encode.go:173`:
  `asUint8(val(WritableAttr{Name: "temp_set"}), val(Writable[7]))` — the two
  arguments are the same lookup; one of them can go.

- [ ] 🟢 **`reflectEqual` compares via `fmt.Sprintf`**
  `internal/web/server.go:395-398` — works for the small type set but is a
  footgun (e.g. `uint8(1)` vs `true` are unequal strings, `1` vs `1.0` differ).
  A typed comparison or `reflect.DeepEqual` after normalization is clearer.

- [x] 🟢 **`indexLastColon` reimplements `strings.LastIndexByte`** /
  `joinSpace` reimplements `strings.Join`.
  *Fixed: `indexLastColon` deleted (clientIP rewrite uses `net.SplitHostPort`);
  `joinSpace` replaced with `strings.Join`.*

- [ ] 🟢 **Port values from env are unvalidated** — `envInt` accepts
  `PORT=-5` or `99999`; fail fast with a clear message.

- [x] 🟢 **`.DS_Store` files are committed** — *correction: they were never
  git-tracked (`.gitignore` already covers them); they only existed as local
  files. Deleted locally; nothing to change in the repo.*

- [ ] 🟢 **README disclaimer tone** — the "absolutely not vibecoded!!!11" line is
  funny but sits right above the security section; consider whether first-time
  visitors read it the way you intend.

## 4. Web UI / design

- [x] 🔴 **Dial assumes °C; breaks in °F mode**
  *Fixed: `setTempRange(unit)` switches the dial between 20–40 °C and 68–104 °F
  (and updates the min/max labels) on every render from `state.temp_unit`.
  No JS test harness exists — verified with a Node smoke test of the extracted
  dial functions (range switch, labels, angle mapping both directions).*

- [ ] 🟡 **Lock state only disables the dial, not the toggles**
  `index.html:439` checks `state.locked` for dial drags, but heater/filter/
  bubbles/power buttons still fire writes while locked. Either grey out all
  controls when locked or let the server reject writes while locked —
  currently the lock is cosmetic in the UI.

- [ ] 🟡 **Replace `alert()`/`prompt()` with in-UI elements**
  Write failures use `alert(e.message)` (raw JSON error bodies), and the auth
  token uses `prompt()`. A small toast + a proper token field (type=password,
  with a way to clear a wrong saved token — today a bad token in localStorage
  re-prompts on every write) would feel much better, especially on iOS where
  these dialogs are jarring.

- [ ] 🟡 **No save feedback / dirty state on the schedule editor**
  Edits mutate the local array silently; "Save" gives no confirmation and there's
  no hint when local edits are unsaved (an SSE-driven refresh never overwrites
  them, but a reload loses them). Disable Save until dirty, flash "Saved ✓",
  and warn on unload with pending edits.

- [ ] 🟡 **Accessibility gaps**
  - Function toggles and power/lock buttons: add `aria-pressed` so state is
    announced.
  - Dial: pointer-only today — add keyboard support (focusable, arrow keys ±1°)
    and `role="slider"` with `aria-valuemin/max/now`.
  - Schedule rows: time selects and remove buttons lack labels
    (`aria-label="Start hour"` etc.).

- [ ] 🟢 **Make it installable (PWA-lite)**
  This is clearly a phone UI: add a web app manifest + apple-touch-icon +
  favicon so "Add to Home Screen" gives a named, full-screen app instead of a
  generic Safari shortcut. (`theme-color` is already set.)

- [ ] 🟢 **Show schedule times in the configured timezone**
  The scheduler runs in `$TZ`, but the timeline/pickers render in the browser's
  local time with no hint. When they differ (e.g. server UTC, phone CEST) a
  17:00 window fires at a different wall-clock time than the UI implies. Either
  surface the server TZ in `/api/config` and label the panel, or document it.

- [ ] 🟢 **Light-mode option** — palette is dark-only; a
  `prefers-color-scheme: light` variant is ~15 lines of CSS variable overrides.

- [ ] 🟢 **Reconnect UX** — on SSE error the whole panel dims via `power-off`,
  which reads as "tub is off" rather than "server unreachable". Distinguish the
  two states (e.g. keep the offline pill but use a different overlay/message).

## 5. Docs

- [ ] 🟢 README: document that `watch`/`state`/`set` CLI need direct LAN access
  to the tub (they don't go through the server) — easy to miss that the CLI
  and `serve` are independent clients that may fight over the tub's single
  TCP session if run simultaneously against the same tub.
- [ ] 🟢 README: note the security model assumes a *segmented* trusted LAN —
  worth one sentence recommending an IoT VLAN given the tub speaks plaintext
  TCP with no auth at all on port 12416.
