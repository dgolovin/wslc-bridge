//go:build windows

package platform

import (
	"context"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
)

func TestPipeAcceptsCurrentUserClient(t *testing.T) {
	pipeName := fmt.Sprintf(`\\.\pipe\wslc_bridge_test_%d`, os.Getpid())
	listener, err := ListenPipe(pipeName)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverDone := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer connection.Close()
		request := make([]byte, len("pipe-ok"))
		if _, err = io.ReadFull(connection, request); err == nil {
			_, err = connection.Write(request)
		}
		serverDone <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connection, err := winio.DialPipeContext(ctx, pipeName)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.Write([]byte("pipe-ok")); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, len("pipe-ok"))
	if _, err := io.ReadFull(connection, response); err != nil {
		t.Fatal(err)
	}
	if string(response) != "pipe-ok" {
		t.Fatalf("pipe returned %q", response)
	}
	_ = connection.Close()
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}
