package xio

import "io"

type halfCloser interface{ CloseWrite() error }

// Splice copies both directions and propagates half-close, so a FIN on one
// side reaches the other instead of leaking a channel.
func Splice(a, b io.ReadWriteCloser) {
	done := make(chan struct{}, 2)
	cp := func(dst, src io.ReadWriteCloser) {
		io.Copy(dst, src)
		if cw, ok := dst.(halfCloser); ok {
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
}
