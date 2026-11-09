package guest

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestStdioHelperProcess(t *testing.T) {
	if os.Getenv("WSLC_BRIDGE_STDIO_HELPER") != "1" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		if line == "\r\n" {
			break
		}
	}
	_, _ = fmt.Fprint(os.Stdout, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nOK")
	_, _ = fmt.Fprint(os.Stderr, "helper diagnostic must not enter HTTP output\n")
	_, _ = io.Copy(io.Discard, reader)
}

func TestStdioDialerCarriesRawBytesAndSeparatesStderr(t *testing.T) {
	dialer := &StdioDialer{
		WSLCPath: os.Args[0],
		Command:  []string{"-test.run=TestStdioHelperProcess"},
		Env:      []string{"WSLC_BRIDGE_STDIO_HELPER=1"},
	}
	connection, err := dialer.DialContext(context.Background(), "tcp", "ignored")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	if err := connection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	request := "GET /_ping HTTP/1.1\r\nHost: docker\r\nConnection: close\r\n\r\n"
	if _, err := io.WriteString(connection, request); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(connection)
	status, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			t.Fatal(readErr)
		}
		if line == "\r\n" {
			break
		}
	}
	body := make([]byte, 2)
	if _, err := io.ReadFull(reader, body); err != nil {
		t.Fatal(err)
	}
	if status != "HTTP/1.1 200 OK\r\n" || string(body) != "OK" {
		t.Fatalf("unexpected response status %q body %q", status, body)
	}
	if strings.Contains(status+string(body), "helper diagnostic") {
		t.Fatal("stderr leaked into the Docker HTTP byte stream")
	}
}
