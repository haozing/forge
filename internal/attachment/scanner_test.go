package attachment

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestClamAVScannerClean(t *testing.T) {
	payload := []byte("ordinary attachment content")
	address, received := fakeClamd(t, "stream: OK\x00")

	err := (ClamAVScanner{Address: address, Timeout: time.Second}).Scan(context.Background(), bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if got := <-received; !bytes.Equal(got, payload) {
		t.Fatalf("clamd payload = %q, want %q", got, payload)
	}
}

func TestClamAVScannerRejectsInfectedObject(t *testing.T) {
	address, received := fakeClamd(t, "stream: Eicar-Signature FOUND\x00")

	err := (ClamAVScanner{Address: address, Timeout: time.Second}).Scan(context.Background(), bytes.NewReader([]byte("infected")))
	if !errors.Is(err, ErrInfected) {
		t.Fatalf("Scan() error = %v, want ErrInfected", err)
	}
	<-received
}

func fakeClamd(t *testing.T, response string) (string, <-chan []byte) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	received := make(chan []byte, 1)
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		command := make([]byte, len("zINSTREAM\x00"))
		if _, err := io.ReadFull(conn, command); err != nil {
			return
		}
		var content bytes.Buffer
		for {
			var size uint32
			if err := binary.Read(conn, binary.BigEndian, &size); err != nil {
				return
			}
			if size == 0 {
				break
			}
			chunk := make([]byte, size)
			if _, err := io.ReadFull(conn, chunk); err != nil {
				return
			}
			_, _ = content.Write(chunk)
		}
		received <- content.Bytes()
		_, _ = conn.Write([]byte(response))
	}()
	return listener.Addr().String(), received
}
