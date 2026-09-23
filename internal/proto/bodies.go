package proto

// HelloRequest is the body of the first request every connection MUST send.
type HelloRequest struct {
	ProtoVersion uint16 `msgpack:"proto_version"`       // client max
	ProtoMin     uint16 `msgpack:"proto_min,omitempty"` // 0 => ProtoVersion
	Agent        string `msgpack:"agent"`
	Vendor       string `msgpack:"vendor"`
	SessionID    string `msgpack:"session_id,omitempty"` // empty => server generates a ULID, forces Ephemeral
	Ephemeral    bool   `msgpack:"ephemeral"`
	PID          int    `msgpack:"pid"`
}

// HelloResponse is the body of a successful hello response.
type HelloResponse struct {
	ProtoVersion  uint16 `msgpack:"proto_version"`
	SessionID     string `msgpack:"session_id"`
	Ephemeral     bool   `msgpack:"ephemeral"`
	ServerPID     int    `msgpack:"server_pid"`
	ServerVersion string `msgpack:"server_version"`
}

// AcquireRequest is the body of a lock.acquire request.
type AcquireRequest struct {
	Root      string `msgpack:"root"`        // absolute, cleaned
	Pattern   string `msgpack:"pattern"`     // repo-relative, normalized
	Mode      string `msgpack:"mode"`        // "exclusive" | "shared"
	TTLMs     int64  `msgpack:"ttl_ms"`      // > 0
	SlotTTLMs int64  `msgpack:"slot_ttl_ms"` // > 0
	WaitMs    int64  `msgpack:"wait_ms"`     // >= 0; 0 = non-blocking poll
}

// AcquireResponse is the body of a lock.acquire response.
type AcquireResponse struct {
	Status    string `msgpack:"status"` // "granted" | "queued"
	Token     uint64 `msgpack:"token,omitempty"`
	ExpiresMs int64  `msgpack:"expires_ms,omitempty"`
	Position  int    `msgpack:"position,omitempty"`
	Root      string `msgpack:"root"`
	Pattern   string `msgpack:"pattern"`
	Mode      string `msgpack:"mode"`
	SessionID string `msgpack:"session_id"`
	Ephemeral bool   `msgpack:"ephemeral"`
	Resumed   bool   `msgpack:"resumed"`
}

// ReleaseRequest is the body of a lock.release request.
type ReleaseRequest struct {
	Token uint64 `msgpack:"token"`
	Root  string `msgpack:"root,omitempty"`
}

// ReleaseResponse is the body of a lock.release response.
type ReleaseResponse struct {
	Token uint64 `msgpack:"token"`
}

// RenewRequest is the body of a lock.renew request.
type RenewRequest struct {
	Token uint64 `msgpack:"token"`
	TTLMs int64  `msgpack:"ttl_ms"`
	Root  string `msgpack:"root,omitempty"`
}

// RenewResponse is the body of a lock.renew response.
type RenewResponse struct {
	Token     uint64 `msgpack:"token"`
	ExpiresMs int64  `msgpack:"expires_ms"`
}

// StatusRequest is the body of a status request. Empty fields mean "no
// filter".
type StatusRequest struct {
	Root      string `msgpack:"root,omitempty"`
	SessionID string `msgpack:"session_id,omitempty"`
}

// StatusResponse is the body of a status response.
type StatusResponse struct {
	ServerPID    int           `msgpack:"server_pid"`
	ProtoVersion uint16        `msgpack:"proto_version"`
	Grants       []GrantInfo   `msgpack:"grants"`
	Queue        []PendingInfo `msgpack:"queue"`
}

// GrantInfo is one active grant in a StatusResponse.
type GrantInfo struct {
	Token     uint64 `msgpack:"token"`
	SessionID string `msgpack:"session_id"`
	Root      string `msgpack:"root"`
	Pattern   string `msgpack:"pattern"`
	Mode      string `msgpack:"mode"`
	ExpiresMs int64  `msgpack:"expires_ms"`
}

// PendingInfo is one queued waiter in a StatusResponse.
type PendingInfo struct {
	SessionID     string `msgpack:"session_id"`
	Root          string `msgpack:"root"`
	Pattern       string `msgpack:"pattern"`
	Mode          string `msgpack:"mode"`
	Position      int    `msgpack:"position"`
	Awaiting      bool   `msgpack:"awaiting"`
	SlotExpiresMs int64  `msgpack:"slot_expires_ms,omitempty"` // 0 while awaiting
}

// GrantedPush is the body of the reserved (never emitted in Phase 2)
// lock.granted push.
type GrantedPush struct {
	Token     uint64 `msgpack:"token"`
	Root      string `msgpack:"root"`
	Pattern   string `msgpack:"pattern"`
	ExpiresMs int64  `msgpack:"expires_ms"`
}
