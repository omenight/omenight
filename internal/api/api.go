package api

import (
	"encoding/json"
	"net"
	"time"
)

// Snapshot is what the daemon serves on its unix socket, one JSON document
// per connection, then close.
type Snapshot struct {
	Endpoint  string    `json:"endpoint"`
	Kind      string    `json:"kind"`
	Version   string    `json:"version"`
	PublicKey string    `json:"public_key"`
	StartedAt time.Time `json:"started_at"`
	SyncError string    `json:"sync_error,omitempty"`
	Clients   []Client  `json:"clients"`
}

type Client struct {
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Online    bool      `json:"online"`
	Iface     string    `json:"iface"`
	Port      int       `json:"port"`
	Mark      int       `json:"mark"`
	Address   string    `json:"address"` // node end of the tunnel
	Endpoint  string    `json:"endpoint,omitempty"`
	Handshake time.Time `json:"handshake,omitempty"`
	Rx        int64     `json:"rx"`
	Tx        int64     `json:"tx"`
}

// Command is a write request. Sending one instead of connecting idle turns
// the snapshot socket into a control socket.
type Command struct {
	Cmd  string `json:"cmd"` // approve | revoke
	Name string `json:"name"`
}

type Response struct {
	OK       bool      `json:"ok"`
	Message  string    `json:"message,omitempty"`
	Snapshot *Snapshot `json:"snapshot,omitempty"`
}

// Fetch reads one snapshot from the daemon's socket.
func Fetch(socket string) (*Snapshot, error) {
	c, err := dial(socket)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	var s Snapshot
	if err := json.NewDecoder(c).Decode(&s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Do sends a command and returns the resulting snapshot.
func Do(socket string, cmd Command) (*Response, error) {
	c, err := dial(socket)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	b, err := json.Marshal(cmd)
	if err != nil {
		return nil, err
	}
	if _, err := c.Write(append(b, '\n')); err != nil {
		return nil, err
	}

	var r Response
	if err := json.NewDecoder(c).Decode(&r); err != nil {
		return nil, err
	}
	return &r, nil
}

func dial(socket string) (net.Conn, error) {
	c, err := net.DialTimeout("unix", socket, 2*time.Second)
	if err != nil {
		return nil, err
	}
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	return c, nil
}
