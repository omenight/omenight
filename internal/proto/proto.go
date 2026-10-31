package proto

// Version is reported in the hello and in the SSH banner.
const Version = "0.1.0"

const (
	// ControlChannel carries newline-delimited JSON in both directions.
	ControlChannel = "omenight-control"
	// Keepalive is answered with "true" by both peers, like OpenSSH.
	Keepalive = "keepalive@openssh.com"
)

// RFC 4254 §7.1
type TCPIPForwardRequest struct {
	BindAddr string
	BindPort uint32
}

type TCPIPForwardReply struct {
	BindPort uint32
}

// RFC 4254 §7.2. Addr/Port must echo the requested bind address verbatim.
type ForwardedTCPIPPayload struct {
	Addr       string
	Port       uint32
	OriginAddr string
	OriginPort uint32
}

const (
	TypeHello  = "hello"
	TypeConfig = "config"
	TypePing   = "ping"
)

// Hello is the agent's first message: what it wants and what it is.
type Hello struct {
	Version  string   `json:"version"`
	Hostname string   `json:"hostname"`
	OS       string   `json:"os"`
	Tunnels  []string `json:"tunnels"` // listen addresses requested on the relay
}

// Config is the relay's grant. Targets stay on the agent: the relay never
// learns where traffic ends up.
type Config struct {
	Granted      []string `json:"granted"`
	HeartbeatSec int      `json:"heartbeat_sec"`
	Reason       string   `json:"reason,omitempty"`
}

type Ping struct {
	TS    int64 `json:"ts"`
	Conns int64 `json:"conns,omitempty"`
}

type Msg struct {
	Type   string  `json:"type"`
	Hello  *Hello  `json:"hello,omitempty"`
	Config *Config `json:"config,omitempty"`
	Ping   *Ping   `json:"ping,omitempty"`
}
