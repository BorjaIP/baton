# Baton Wire Protocol (v1)

This document describes the protocol spoken between `baton serve` (the lock
daemon, `internal/server`) and every client (`internal/client`, used by
`internal/adapters/ctl`). It is implemented by `internal/proto`.

## 1. Transport and Address

The daemon listens on a Unix domain socket.

- **Path resolution**: `$BATON_SOCK` if set (must be an absolute path),
  otherwise `~/.baton/baton.sock`.
- **Directory**: the containing directory is created with mode `0700` if it
  does not already exist.
- **Socket permissions**: the socket file itself is `chmod`ed to `0600`
  after binding.
- **Derived paths**: the single-instance flock file and the autostart log
  file are derived from the socket's base name by replacing the `.sock`
  suffix: `<base>.lock` and `<base>.log`.
- **Path length limit**: the resolved socket path must be no longer than
  103 bytes (`proto.MaxSocketPath`), chosen to stay portable across
  platforms (macOS `sockaddr_un.sun_path` is 104 bytes including the
  trailing NUL, Linux's is 108).

A socket file's mere existence never indicates a live daemon. Only a
successful dial followed by a successful `hello` exchange proves liveness.

## 2. Framing

Every message on the wire is framed as:

```
+----------------------------+----------------------------------+
| 4-byte big-endian length N | N bytes of msgpack-encoded payload |
+----------------------------+----------------------------------+
```

- **Maximum frame size**: `1 MiB` (`proto.MaxFrameSize = 1 << 20`).
- **Oversize frames**: a declared length greater than the maximum is
  rejected with `ErrFrameTooLarge` *before* any buffer is allocated for the
  payload. The server logs this and closes the connection, since the byte
  stream cannot be cheaply resynchronized.
- **Truncated frames**: if fewer than N payload bytes arrive before the
  connection closes or times out, `ErrFrameTruncated` is returned (not a
  partial decode).
- **Empty frames**: a declared length of `0` is rejected with
  `ErrFrameEmpty`.
- **Malformed payload**: once a complete frame is read, if the payload does
  not decode as valid msgpack, or decodes but is missing a required
  envelope field, decoding returns `ErrMalformedPayload` or
  `ErrInvalidEnvelope` respectively. The frame boundary is still intact, so
  the connection is *not* closed for this case — an `invalid_envelope`
  error response is sent and the connection loop continues.

## 3. Envelope

```go
type Envelope struct {
    ID   string             // correlation id
    Kind Kind                // "request" | "response" | "push"
    Verb string              // required for request/push; echoed on response
    Body msgpack.RawMessage  // verb-specific payload
    Err  *Error              // response only; Body is empty when set
}
```

- **`id` rules**: clients use a decimal counter starting at 1 for each
  request they send. A `response` envelope's `id` equals the `id` of the
  request it answers. A `push` envelope carries a server-assigned id
  distinct from any pending request id (reserved for future use; see §8).
- **Unknown fields are ignored** by the msgpack decoder, keeping the
  protocol forward-compatible.
- **Kind validation**: a `request` requires `id` and `verb`; a `response`
  requires `id`; a `push` requires `verb`. Violating this yields
  `invalid_envelope`.

## 4. Handshake (`hello`)

The first message a client sends on a new connection MUST be a `hello`
request:

| Field | Type | Meaning |
|---|---|---|
| `proto_version` | uint16 | client's maximum supported version |
| `proto_min` | uint16 | client's minimum supported version (0 => same as `proto_version`) |
| `agent` | string | client identifier (e.g. `baton-ctl`) |
| `vendor` | string | vendor identifier |
| `session_id` | string | stable session id, or empty for an ephemeral session |
| `ephemeral` | bool | true if `session_id` was not explicitly supplied |
| `pid` | int | client process id |

Response:

| Field | Type | Meaning |
|---|---|---|
| `proto_version` | uint16 | negotiated version |
| `session_id` | string | echoed, or server-generated if the client sent none |
| `ephemeral` | bool | echoed |
| `server_pid` | int | daemon process id |
| `server_version` | string | daemon build identifier |

**Negotiation**: the server supports `[MinVersion, Version]` (currently
`[1, 1]`). The negotiated version is
`min(clientMax, serverMax)` when it is `>= max(clientMin, serverMin)`.
Otherwise the server responds with a `version_mismatch` error naming both
the client's and the server's supported ranges in `Error.Detail`. **The
connection is not closed** solely because of a version mismatch; the
client decides whether to disconnect.

**Pre-hello rules**: any verb other than `hello` sent before the handshake
completes gets `hello_required`. A second `hello` on an already
hello-completed connection gets `invalid_request`. Each pre-hello frame is
subject to a bounded read deadline (`HelloTimeout`, default 5s); after
hello succeeds, this deadline is cleared.

## 5. Connection Rules

- **One in-flight request per connection**: a second request arriving
  while one is still being handled gets `request_in_flight` immediately.
- **Disconnect semantics — stable session**: if the connection's session
  was established with an explicit `session_id`, disconnecting does
  **not** cancel any of that session's pending (queued) waiters. The slot
  is kept, governed by its own `SlotTTL`, and can be resumed by a later
  connection using the same session id and normalized pattern.
- **Disconnect semantics — ephemeral session**: if the connection is
  ephemeral (no explicit `session_id`), disconnecting while a pending
  (not-yet-granted) waiter is registered for that connection cancels that
  waiter immediately, freeing the queue slot. A grant already delivered in
  a response before disconnect is **not** cancelled — it remains governed
  by TTL/release/renew only.
- **No `ReleaseSession` on disconnect**: closing a connection never
  releases an already-granted lock, for either stable or ephemeral
  sessions. Grants outlive the connection that acquired them.

## 6. Verbs

### 6.1 `status`

Request:

| Field | Type | Meaning |
|---|---|---|
| `root` | string (optional) | filter to one repository root; empty = no filter |
| `session_id` | string (optional) | filter to one session; empty = no filter |

Response:

| Field | Type | Meaning |
|---|---|---|
| `server_pid` | int | daemon process id |
| `proto_version` | uint16 | negotiated protocol version |
| `grants` | []GrantInfo | active grants (token, session, root, pattern, mode, expires) |
| `queue` | []PendingInfo | queued waiters (session, root, pattern, mode, position, awaiting, slot_expires) |

`status` never mutates grant/queue state beyond the standard lazy sweep of
expired entries.

### 6.2 `lock.acquire`

Request:

| Field | Type | Meaning |
|---|---|---|
| `root` | string | absolute, cleaned repository root |
| `pattern` | string | repo-relative, normalized pattern |
| `mode` | string | `"exclusive"` or `"shared"` |
| `ttl_ms` | int64 | grant TTL once acquired, > 0 |
| `slot_ttl_ms` | int64 | queued-slot TTL, > 0 |
| `wait_ms` | int64 | how long to block waiting for a grant; 0 = non-blocking poll |

Response:

| Field | Type | Meaning |
|---|---|---|
| `status` | string | `"granted"` or `"queued"` |
| `token` | uint64 | fencing token (granted only) |
| `expires_ms` | int64 | grant expiry, Unix ms (granted only) |
| `position` | int | FIFO queue position (queued only) |
| `root`, `pattern`, `mode` | string | echoed scope |
| `session_id` | string | echoed session |
| `ephemeral` | bool | echoed |
| `resumed` | bool | true if an existing waiter was resumed rather than freshly enqueued |

**Resume**: a stable session calling `lock.acquire` again for the same
normalized pattern and mode resumes its existing waiter rather than
enqueuing a new one; no `session_overlap` error results. On resume, the new
request's `ttl_ms`/`slot_ttl_ms` are **ignored** — the existing waiter keeps
its original parameters. Use `lock.renew` to extend an active grant.

**Idempotent re-acquire (G2)**: if the resumed waiter has already been
granted, the response repeats the existing grant and token unchanged
(no new token is minted, no error).

**Terminal re-enqueue**: if a registered waiter has become terminal (its
slot expired, or it was cancelled), the stale entry is discarded and a
fresh `Enqueue` is attempted transparently.

**Wait semantics**: with `wait_ms = 0`, a busy pattern returns `"queued"`
immediately. With `wait_ms > 0`, the call blocks (subject to the
connection's own lifetime) until granted, the wait elapses, or the
connection's context is cancelled (disconnect or shutdown).

### 6.3 `lock.release`

Request: `token` (uint64), `root` (string, optional).

Response: `token` (uint64, echoed).

If `root` is supplied and does not match the token's stored root, the
server responds `root_mismatch` instead of releasing.

### 6.4 `lock.renew`

Request: `token` (uint64), `ttl_ms` (int64, > 0), `root` (string, optional).

Response: `token` (uint64, echoed), `expires_ms` (int64, new expiry).

Same `root_mismatch` check as `lock.release`.

## 7. Repository Scoping

`lock.acquire`, `lock.release`, `lock.renew`, and `status` all carry an
absolute repository root alongside the repo-relative pattern. The server
composes these into a single internal key so that identical relative
patterns in different repositories never conflict with each other.

**Client contract**: `internal/adapters/ctl` normalizes every pattern
before sending it (`NormalizePattern`): OS separators become `/`, a
leading `./` is stripped, an absolute path is made root-relative, and
patterns that would escape the root (`..`, or a `../` prefix) are rejected
client-side, never reaching the wire.

**Server validation**: the server re-validates every `root`/`pattern` pair
with the same `proto.ValidateScope` function as defense in depth. It never
transforms input — a non-normalized pattern is rejected with
`invalid_request`.

`status` output reports the relative pattern and, in `--json` mode, the
repository root as a separate field — never the composed internal key.

## 8. Push Messages

The envelope `kind` enumeration includes `push`, and the verb set reserves
`lock.granted` as a defined push verb. **In this version, the server never
emits `lock.granted`** (or any other push) — a grant obtained during a
blocked wait is delivered only as the `lock.acquire` response (design
decision G4). Clients MUST still tolerate receiving an unexpected `push`
envelope (e.g. by ignoring unrecognized push verbs) without treating it as
a protocol error, since this is defined for forward compatibility.

## 9. Error Codes

Every relevant `internal/core/lock` sentinel error maps to exactly one
distinct wire error code. Two different core sentinels never share a code.

| Wire code | Meaning | Core sentinel | ctl exit code |
|---|---|---|---|
| `invalid_envelope` | malformed or invalid message envelope | — | 1 |
| `invalid_request` | invalid request parameters (validation, scope, decode) | `ErrInvalidRequest` | 2 |
| `unknown_verb` | unrecognized verb | — | 1 |
| `hello_required` | a non-hello verb was sent before handshake | — | 1 |
| `version_mismatch` | no common protocol version | — | 1 |
| `request_in_flight` | a request is already in flight on this connection | — | 4 |
| `session_overlap` | session already holds/queues an overlapping pattern | `ErrSessionOverlap` | 4 |
| `stale_token` | token superseded by a later grant | `ErrStaleToken` | 4 |
| `unknown_token` | token not recognized | `ErrUnknownToken` | 4 |
| `slot_expired` | queued slot expired before being granted | `ErrSlotExpired` | 4 |
| `waiter_closed` | queued wait was cancelled | `ErrWaiterClosed` | 4 |
| `await_busy` | another wait already in progress for this waiter | `ErrAwaitBusy` | 4 |
| `root_mismatch` | token belongs to a different repository root | — | 4 |
| `shutting_down` | daemon is shutting down | — | 5 |
| `internal` | internal server error | (any other) | 1 |

Additionally, `ctl` uses exit code `3` for a successful `lock.acquire` that
returned `"queued"` (not an error), `5` for a daemon that could not be
reached (dial and autostart both failed), and `130` for an interrupted
wait (SIGINT/SIGTERM), a shell convention outside this error-code table.

## 10. Versioning Policy

This document describes protocol version `1` (`proto.Version`,
`proto.MinVersion`). A future incompatible change bumps `Version` and, if
older servers/clients must still interoperate during a transition,
`MinVersion` stays at the oldest version still supported, letting
`Negotiate` pick the highest mutually supported version. Every wire-visible
change (new required field, new verb, changed semantics) must be reflected
in this document and, for a breaking change, in the version range above.

### Change Log

- **v1** (this document): initial Phase 2 protocol — `hello`, `status`,
  `lock.acquire`, `lock.release`, `lock.renew`; `lock.granted` push
  reserved but unused.
