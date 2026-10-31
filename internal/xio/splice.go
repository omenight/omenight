package xio

import "io"

type halfCloser interface{ CloseWrite() error }

// Splice copies both directions and propagates half-close, so a FIN on one
// side reaches the other instead of leaking a channel. Returns bytes
// sent a->b and b->a.
func Splice(a, b io.ReadWriteCloser) (int64, int64) {
	type result struct {
		dir int
		n   int64
	}
	done := make(chan result, 2)
	cp := func(dir int, dst, src io.ReadWriteCloser) {
		n, _ := io.Copy(dst, src)
		if cw, ok := dst.(halfCloser); ok {
			cw.CloseWrite()
		} else {
			dst.Close()
		}
		done <- result{dir, n}
	}
	go cp(0, a, b)
	go cp(1, b, a)

	var ab, ba int64
	for i := 0; i < 2; i++ {
		r := <-done
		if r.dir == 0 {
			ab = r.n
		} else {
			ba = r.n
		}
	}
	return ab, ba
}
