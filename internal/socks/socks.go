package socks

import (
	"encoding/binary"
	"errors"
	"io"
	"log"
	"net"
	"sync/atomic"
	"time"
)

const (
	repSuccess        = 0x00
	repConnRefused    = 0x05
	repCmdNotSupport  = 0x07
	repAddrNotSupport = 0x08
)

// Server is a CONNECT-only SOCKS5 proxy. It dials from the host it runs on,
// which is the whole point: it lives on the egress node.
type Server struct {
	ln      net.Listener
	verbose bool
	active  atomic.Int64
}

func Listen(addr string, verbose bool) (*Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &Server{ln: ln, verbose: verbose}, nil
}

func (s *Server) Addr() net.Addr { return s.ln.Addr() }
func (s *Server) Close() error   { return s.ln.Close() }

func (s *Server) Serve() error {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			log.Printf("socks accept: %v", err)
			continue
		}
		go s.handle(c)
	}
}

func (s *Server) handle(c net.Conn) {
	defer c.Close()

	c.SetDeadline(time.Now().Add(20 * time.Second))
	if err := negotiate(c); err != nil {
		return
	}
	target, err := request(c)
	if err != nil {
		return
	}
	c.SetDeadline(time.Time{})

	up, err := net.DialTimeout("tcp", target, 10*time.Second)
	if err != nil {
		reply(c, repConnRefused, nil)
		return
	}
	defer up.Close()

	reply(c, repSuccess, up.LocalAddr())
	s.active.Add(1)
	defer s.active.Add(-1)

	n := splice(c, up)
	if s.verbose {
		log.Printf("socks %s -> %s %d bytes", c.RemoteAddr(), target, n)
	}
}

func negotiate(c net.Conn) error {
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(c, hdr); err != nil {
		return err
	}
	if hdr[0] != 5 {
		return errors.New("not socks5")
	}
	methods := make([]byte, int(hdr[1]))
	if _, err := io.ReadFull(c, methods); err != nil {
		return err
	}
	_, err := c.Write([]byte{5, 0x00}) // no auth
	return err
}

func request(c net.Conn) (string, error) {
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(c, hdr); err != nil {
		return "", err
	}
	if hdr[0] != 5 {
		return "", errors.New("bad version")
	}
	if hdr[1] != 0x01 {
		reply(c, repCmdNotSupport, nil)
		return "", errors.New("unsupported command")
	}

	var host string
	switch hdr[3] {
	case 0x01:
		b := make([]byte, 4)
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		host = net.IP(b).String()
	case 0x04:
		b := make([]byte, 16)
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		host = net.IP(b).String()
	case 0x03:
		l := make([]byte, 1)
		if _, err := io.ReadFull(c, l); err != nil {
			return "", err
		}
		b := make([]byte, int(l[0]))
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		host = string(b)
	default:
		reply(c, repAddrNotSupport, nil)
		return "", errors.New("bad address type")
	}

	p := make([]byte, 2)
	if _, err := io.ReadFull(c, p); err != nil {
		return "", err
	}
	port := binary.BigEndian.Uint16(p)
	return net.JoinHostPort(host, itoa(int(port))), nil
}

func reply(c net.Conn, code byte, bound net.Addr) {
	var bnd []byte
	if ta, ok := bound.(*net.TCPAddr); ok {
		if ip4 := ta.IP.To4(); ip4 != nil {
			bnd = append([]byte{0x01}, ip4...)
		} else {
			bnd = append([]byte{0x04}, ta.IP.To16()...)
		}
		bnd = append(bnd, byte(ta.Port>>8), byte(ta.Port))
	} else {
		bnd = []byte{0x01, 0, 0, 0, 0, 0, 0}
	}
	c.Write(append([]byte{5, code, 0}, bnd...))
}

func splice(a, b net.Conn) int64 {
	var n atomic.Int64
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		c, _ := io.Copy(dst, src)
		n.Add(c)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		} else {
			dst.Close()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
	return n.Load()
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [6]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}
