package store

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	"omenight/internal/wg"
)

const (
	StatusActive  = "active"
	StatusRevoked = "revoked"
)

// Settings are fixed by `init`. Every node's interface, port, addresses and
// mark derive from its slot through them, so they never change afterwards.
type Settings struct {
	Kind       string     `json:"kind"`
	Endpoint   string     `json:"endpoint"` // host nodes dial, without a port
	Net        string     `json:"net"`      // carved into one /30 per slot
	PortBase   int        `json:"port_base"`
	MarkBase   int        `json:"mark_base"` // fwmark and routing table
	Prefix     string     `json:"iface_prefix"`
	PrivateKey string     `json:"private_key"`
	AWG        *wg.Params `json:"awg,omitempty"`
}

func (s Settings) Iface(slot int) string { return s.Prefix + strconv.Itoa(slot) }
func (s Settings) Port(slot int) int     { return s.PortBase + slot }
func (s Settings) Mark(slot int) int     { return s.MarkBase + slot }

func (s Settings) RelayAddr(slot int) netip.Prefix { return s.addr(slot, 1) }
func (s Settings) NodeAddr(slot int) netip.Prefix  { return s.addr(slot, 2) }

func (s Settings) addr(slot, off int) netip.Prefix {
	p := netip.MustParsePrefix(s.Net).Masked()
	b := p.Addr().As4()
	binary.BigEndian.PutUint32(b[:], binary.BigEndian.Uint32(b[:])+uint32(slot*4+off))
	return netip.PrefixFrom(netip.AddrFrom4(b), 30)
}

// Slots is how many /30s fit in Net, slot 0 left unused.
func (s Settings) Slots() int {
	p := netip.MustParsePrefix(s.Net)
	n := 1<<(32-p.Bits())/4 - 1
	return min(n, 65535-s.PortBase)
}

func (s Settings) EndpointFor(slot int) string {
	return net.JoinHostPort(s.Endpoint, strconv.Itoa(s.Port(slot)))
}

func (s Settings) Validate() error {
	if s.Kind != wg.KindAWG && s.Kind != wg.KindWG {
		return fmt.Errorf("kind must be %s or %s", wg.KindAWG, wg.KindWG)
	}
	if _, err := netip.ParseAddr(s.Endpoint); err != nil && !hostRe.MatchString(s.Endpoint) {
		return fmt.Errorf("endpoint %q: want a hostname or an IP, without a port", s.Endpoint)
	}
	p, err := netip.ParsePrefix(s.Net)
	if err != nil || !p.Addr().Is4() || p.Bits() > 28 {
		return fmt.Errorf("net %q: want an IPv4 prefix of /28 or wider", s.Net)
	}
	if s.PortBase < 1 || s.PortBase > 65000 {
		return fmt.Errorf("port base %d out of range", s.PortBase)
	}
	if s.MarkBase < 1 {
		return errors.New("mark base must be positive")
	}
	if s.Prefix == "" || len(s.Prefix) > 8 {
		return errors.New("interface prefix must be 1-8 characters")
	}
	return wg.CheckKey(s.PrivateKey)
}

type Client struct {
	Name       string    `json:"name"`
	Slot       int       `json:"slot"`
	Status     string    `json:"status"`
	PrivateKey string    `json:"private_key"` // kept so the paste block can be printed again
	PublicKey  string    `json:"public_key"`
	CreatedAt  time.Time `json:"created_at"`
}

type document struct {
	Settings *Settings `json:"settings,omitempty"`
	Clients  []Client  `json:"clients"`
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

var ErrNoSettings = errors.New("relay is not initialized, run `omenight-relay init` first")

func (s *Store) Settings() (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.doc.Settings == nil {
		return Settings{}, ErrNoSettings
	}
	return *s.doc.Settings, nil
}

func (s *Store) Init(set Settings) error {
	if err := set.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.doc.Settings != nil {
		return fmt.Errorf("%s is already initialized", s.path)
	}
	s.doc.Settings = &set
	return s.save()
}

func (s *Store) indexByName(name string) int {
	for i := range s.doc.Clients {
		if s.doc.Clients[i].Name == name {
			return i
		}
	}
	return -1
}

// Both end up inside a shell script on the node, so they stay quote-free.
var (
	nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)
	hostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,252}[A-Za-z0-9])?$`)
)

// Add creates an active client on the lowest free slot.
func (s *Store) Add(name, priv, pub string) (Client, error) {
	if !nameRe.MatchString(name) {
		return Client{}, fmt.Errorf("bad name %q: letters, digits, '.', '_', '-', up to 32", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.doc.Settings == nil {
		return Client{}, ErrNoSettings
	}
	if s.indexByName(name) >= 0 {
		return Client{}, fmt.Errorf("%s already exists", name)
	}
	used := map[int]bool{}
	for _, c := range s.doc.Clients {
		used[c.Slot] = true
	}
	slot := 0
	for i := 1; i <= s.doc.Settings.Slots(); i++ {
		if !used[i] {
			slot = i
			break
		}
	}
	if slot == 0 {
		return Client{}, errors.New("no free slot left")
	}
	c := Client{
		Name:       name,
		Slot:       slot,
		Status:     StatusActive,
		PrivateKey: priv,
		PublicKey:  pub,
		CreatedAt:  time.Now().UTC(),
	}
	s.doc.Clients = append(s.doc.Clients, c)
	return c, s.save()
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
		return Client{}, fmt.Errorf("%s: no such client", name)
	}
	s.doc.Clients[i].Status = status
	return s.doc.Clients[i], s.save()
}

// Remove deletes the client and frees its slot for the next Add.
func (s *Store) Remove(name string) (Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.indexByName(name)
	if i < 0 {
		return Client{}, fmt.Errorf("%s: no such client", name)
	}
	c := s.doc.Clients[i]
	s.doc.Clients = append(s.doc.Clients[:i], s.doc.Clients[i+1:]...)
	return c, s.save()
}
