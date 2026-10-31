package wg

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// GenerateKey returns a private key in the format `wg genkey` prints.
func GenerateKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[0] &= 248
	b[31] = (b[31] & 127) | 64
	return base64.StdEncoding.EncodeToString(b), nil
}

// PublicKey is `wg pubkey`.
func PublicKey(priv string) (string, error) {
	b, err := decodeKey(priv)
	if err != nil {
		return "", err
	}
	k, err := ecdh.X25519().NewPrivateKey(b)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(k.PublicKey().Bytes()), nil
}

// CheckKey rejects anything that is not a base64 curve25519 key, so a
// typo never reaches the kernel as a peer.
func CheckKey(s string) error {
	_, err := decodeKey(s)
	return err
}

func decodeKey(s string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != 32 {
		return nil, fmt.Errorf("not a WireGuard key: %q", s)
	}
	return b, nil
}

// Params are the AmneziaWG 1.0 obfuscation knobs. Both ends must agree on
// them; every AWG version understands this set.
type Params struct {
	Jc   int    `json:"jc"`
	Jmin int    `json:"jmin"`
	Jmax int    `json:"jmax"`
	S1   int    `json:"s1"`
	S2   int    `json:"s2"`
	H1   uint32 `json:"h1"`
	H2   uint32 `json:"h2"`
	H3   uint32 `json:"h3"`
	H4   uint32 `json:"h4"`
}

// NewParams picks values inside the ranges the AmneziaWG docs recommend.
// S1+56 must differ from S2, or init and response packets get the same size.
func NewParams() (Params, error) {
	p := Params{Jmin: 40, Jmax: 70}
	var err error
	if p.Jc, err = randIn(4, 8); err != nil {
		return p, err
	}
	for {
		if p.S1, err = randIn(15, 150); err != nil {
			return p, err
		}
		if p.S2, err = randIn(15, 150); err != nil {
			return p, err
		}
		if p.S1+56 != p.S2 {
			break
		}
	}
	seen := map[uint32]bool{}
	for _, h := range []*uint32{&p.H1, &p.H2, &p.H3, &p.H4} {
		for {
			v, err := randIn(5, 1<<31-1)
			if err != nil {
				return p, err
			}
			if !seen[uint32(v)] {
				seen[uint32(v)] = true
				*h = uint32(v)
				break
			}
		}
	}
	return p, nil
}

// Args is the tail of `awg set`.
func (p Params) Args() []string {
	return []string{
		"jc", strconv.Itoa(p.Jc), "jmin", strconv.Itoa(p.Jmin), "jmax", strconv.Itoa(p.Jmax),
		"s1", strconv.Itoa(p.S1), "s2", strconv.Itoa(p.S2),
		"h1", fmt.Sprint(p.H1), "h2", fmt.Sprint(p.H2), "h3", fmt.Sprint(p.H3), "h4", fmt.Sprint(p.H4),
	}
}

// String is the comma list install.sh takes as --awg, in Args order.
func (p Params) String() string {
	return fmt.Sprintf("%d,%d,%d,%d,%d,%d,%d,%d,%d",
		p.Jc, p.Jmin, p.Jmax, p.S1, p.S2, p.H1, p.H2, p.H3, p.H4)
}

func randIn(lo, hi int64) (int, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(hi-lo+1))
	if err != nil {
		return 0, err
	}
	return int(n.Int64() + lo), nil
}
