package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	StatusPending = "pending"
	StatusActive  = "active"
	StatusRevoked = "revoked"
)

type Client struct {
	Name        string    `json:"name"`
	Fingerprint string    `json:"fingerprint"`
	Status      string    `json:"status"`
	Allow       []string  `json:"allow,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	LastSeen    time.Time `json:"last_seen,omitempty"`
}

type document struct {
	Clients []Client `json:"clients"`
}

// Store is a JSON file with atomic saves. At the scale this runs at (a
// handful of nodes) a database would be the bigger dependency, not the
// simpler one.
type Store struct {
	mu   sync.Mutex
	path string
	doc  document
	mod  time.Time
}

func Open(path string) (*Store, error) {
	s := &Store{path: path}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	if len(b) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(b, &s.doc); err != nil {
		return nil, err
	}
	if st, err := os.Stat(path); err == nil {
		s.mod = st.ModTime()
	}
	return s, nil
}

// Reload picks up edits made by the CLI in another process. Returns true if
// the file changed since the last load.
func (s *Store) Reload() (bool, error) {
	st, err := os.Stat(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if st.ModTime().Equal(s.mod) {
		return false, nil
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return false, err
	}
	var doc document
	if err := json.Unmarshal(b, &doc); err != nil {
		return false, err
	}
	s.doc = doc
	s.mod = st.ModTime()
	return true, nil
}

func (s *Store) Path() string { return s.path }

func (s *Store) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.doc, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	if st, err := os.Stat(s.path); err == nil {
		s.mod = st.ModTime()
	}
	return nil
}

func (s *Store) index(fp string) int {
	for i := range s.doc.Clients {
		if s.doc.Clients[i].Fingerprint == fp {
			return i
		}
	}
	return -1
}

func (s *Store) indexByName(name string) int {
	for i := range s.doc.Clients {
		if s.doc.Clients[i].Name == name {
			return i
		}
	}
	return -1
}

// Register returns the client for fp, creating it as pending on first sight.
// The SSH username is used as the initial label; an admin renames if needed.
func (s *Store) Register(name, fp string) (Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if i := s.index(fp); i >= 0 {
		return s.doc.Clients[i], nil
	}
	c := Client{
		Name:        name,
		Fingerprint: fp,
		Status:      StatusPending,
		CreatedAt:   time.Now().UTC(),
	}
	s.doc.Clients = append(s.doc.Clients, c)
	return c, s.save()
}

func (s *Store) ByFingerprint(fp string) (Client, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := s.index(fp); i >= 0 {
		return s.doc.Clients[i], true
	}
	return Client{}, false
}

func (s *Store) ByName(name string) (Client, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := s.indexByName(name); i >= 0 {
		return s.doc.Clients[i], true
	}
	return Client{}, false
}

func (s *Store) All() []Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Client, len(s.doc.Clients))
	copy(out, s.doc.Clients)
	return out
}

func (s *Store) SetStatus(name, status string) (Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.indexByName(name)
	if i < 0 {
		return Client{}, os.ErrNotExist
	}
	s.doc.Clients[i].Status = status
	return s.doc.Clients[i], s.save()
}

// Allow appends a listen address the client may bind on the relay.
func (s *Store) Allow(name, addr string) (Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.indexByName(name)
	if i < 0 {
		return Client{}, os.ErrNotExist
	}
	for _, a := range s.doc.Clients[i].Allow {
		if a == addr {
			return s.doc.Clients[i], nil
		}
	}
	s.doc.Clients[i].Allow = append(s.doc.Clients[i].Allow, addr)
	return s.doc.Clients[i], s.save()
}

// Touch records liveness without a disk write on every heartbeat.
func (s *Store) Touch(fp string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := s.index(fp); i >= 0 {
		s.doc.Clients[i].LastSeen = time.Now().UTC()
	}
}

// Flush persists in-memory changes (call on shutdown).
func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.save()
}
