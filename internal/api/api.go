package api

import (
	"encoding/json"
	"net"
	"time"
)

// Snapshot is what the daemon serves on its unix socket: one JSON document
// per connection, then close.
type Snapshot struct {
	Listen    string    `json:"listen"`
	Version   string    `json:"version"`
	HostKey   string    `json:"host_key"`
	StartedAt time.Time `json:"started_at"`
	Clients   []Client  `json:"clients"`
}

type Client struct {
	Name     string    `json:"name"`
	Status   string    `json:"status"`
	Online   bool      `json:"online"`
	Remote   string    `json:"remote,omitempty"`
	Since    time.Time `json:"since,omitempty"`
	Conns    int64     `json:"conns"`
	Up       int64     `json:"up"`
	Down     int64     `json:"down"`
	Allow    []string  `json:"allow,omitempty"`
	LastSeen time.Time `json:"last_seen,omitempty"`
}

// Command is a write request. Sending one instead of connecting idle turns
// the snapshot socket into a control socket.
type Command struct {
	Cmd  string `json:"cmd"` // approve | revoke | allow
	Name string `json:"name"`
	Addr string `json:"addr,omitempty"`
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
