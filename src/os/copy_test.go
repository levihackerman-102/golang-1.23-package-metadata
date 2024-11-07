// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package os_test

import (
	"bytes"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"runtime"
	"sync"
	"testing"

	"golang.org/x/net/nettest"
)

// Exercise sendfile/splice fast paths with a moderately large file.
//
// https://go.dev/issue/70000

func TestLargeCopyViaNetwork(t *testing.T) {
	const size = 10 * 1024 * 1024
	dir := t.TempDir()

	src, err := os.Create(dir + "/src")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if _, err := io.CopyN(src, newRandReader(), size); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Seek(0, 0); err != nil {
		t.Fatal(err)
	}

	dst, err := os.Create(dir + "/dst")
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()

	client, server := createSocketPair(t, "tcp")
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if n, err := io.Copy(dst, server); n != size || err != nil {
			t.Errorf("copy to destination = %v, %v; want %v, nil", n, err, size)
		}
	}()
	go func() {
		defer wg.Done()
		defer client.Close()
		if n, err := io.Copy(client, src); n != size || err != nil {
			t.Errorf("copy from source = %v, %v; want %v, nil", n, err, size)
		}
	}()
	wg.Wait()

	if _, err := dst.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	if err := compareReaders(dst, io.LimitReader(newRandReader(), size)); err != nil {
		t.Fatal(err)
	}
}

func compareReaders(a, b io.Reader) error {
	bufa := make([]byte, 4096)
	bufb := make([]byte, 4096)
	for {
		na, erra := io.ReadFull(a, bufa)
		if erra != nil && erra != io.EOF {
			return erra
		}
		nb, errb := io.ReadFull(b, bufb)
		if errb != nil && errb != io.EOF {
			return errb
		}
		if !bytes.Equal(bufa[:na], bufb[:nb]) {
			return errors.New("contents mismatch")
		}
		if erra == io.EOF && errb == io.EOF {
			break
		}
	}
	return nil
}

type randReader struct {
	rand *rand.Rand
}

func newRandReader() *randReader {
	return &randReader{rand.New(rand.NewPCG(0, 0))}
}

func (r *randReader) Read(p []byte) (int, error) {
	var v uint64
	var n int
	for i := range p {
		if n == 0 {
			v = r.rand.Uint64()
			n = 8
		}
		p[i] = byte(v & 0xff)
		v >>= 8
		n--
	}
	return len(p), nil
}

func createSocketPair(t *testing.T, proto string) (client, server net.Conn) {
	t.Helper()
	if !nettest.TestableNetwork(proto) {
		t.Skipf("%s does not support %q", runtime.GOOS, proto)
	}

	ln, err := nettest.NewLocalListener(proto)
	if err != nil {
		t.Fatalf("NewLocalListener error: %v", err)
	}
	t.Cleanup(func() {
		if ln != nil {
			ln.Close()
		}
		if client != nil {
			client.Close()
		}
		if server != nil {
			server.Close()
		}
	})
	ch := make(chan struct{})
	go func() {
		var err error
		server, err = ln.Accept()
		if err != nil {
			t.Errorf("Accept new connection error: %v", err)
		}
		ch <- struct{}{}
	}()
	client, err = net.Dial(proto, ln.Addr().String())
	<-ch
	if err != nil {
		t.Fatalf("Dial new connection error: %v", err)
	}
	return client, server
}