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

// Fetch reads one snapshot from the daemon's socket.
func Fetch(socket string) (*Snapshot, error) {
	c, err := net.DialTimeout("unix", socket, 2*time.Second)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(3 * time.Second))

	var s Snapshot
	if err := json.NewDecoder(c).Decode(&s); err != nil {
		return nil, err
	}
	return &s, nil
}
