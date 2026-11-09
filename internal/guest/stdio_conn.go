package guest

import (
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
	"time"
)

const stderrDrainLimit = 64 << 10

type StdioDialer struct {
	WSLCPath string
	Command  []string
	Env      []string
}

func (d *StdioDialer) DialContext(ctx context.Context, _ string, _ string) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if d.WSLCPath == "" {
		return nil, &net.DNSError{Err: "wslc.exe not found", Name: "guest-docker"}
	}
	processCtx, cancel := context.WithCancel(context.Background())
	arguments := d.Command
	if arguments == nil {
		arguments = []string{
			"system", "session", "run", "docker",
			"-H", "unix:///var/run/docker.sock", "system", "dial-stdio",
		}
	}
	cmd := exec.CommandContext(processCtx, d.WSLCPath, arguments...)
	if len(d.Env) > 0 {
		cmd.Env = append(os.Environ(), d.Env...)
	}
	cmd.WaitDelay = 3 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		cancel()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		cancel()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		cancel()
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		_ = cmd.Wait()
		return nil, err
	}
	clientEnd, processEnd := net.Pipe()
	connection := &stdioConn{
		Conn: clientEnd, processEnd: processEnd,
		cmd: cmd, cancel: cancel, stdin: stdin, stdout: stdout, stderr: stderr,
		waitDone: make(chan struct{}), outputDone: make(chan struct{}), stderrDone: make(chan struct{}),
	}
	go connection.writeStdin()
	go connection.readStdout()
	go connection.drainStderr()
	go connection.wait()
	return connection, nil
}

type stdioConn struct {
	net.Conn
	processEnd net.Conn
	cmd        *exec.Cmd
	cancel     context.CancelFunc
	stdin      io.WriteCloser
	stdout     io.ReadCloser
	stderr     io.ReadCloser
	waitDone   chan struct{}
	outputDone chan struct{}
	stderrDone chan struct{}
	closeOnce  sync.Once
}

func (c *stdioConn) writeStdin() {
	_, _ = io.Copy(c.stdin, c.processEnd)
	_ = c.stdin.Close()
}

func (c *stdioConn) readStdout() {
	_, _ = io.Copy(c.processEnd, c.stdout)
	_ = c.processEnd.Close()
	close(c.outputDone)
}

func (c *stdioConn) drainStderr() {
	_, _ = io.Copy(io.Discard, io.LimitReader(c.stderr, stderrDrainLimit))
	_, _ = io.Copy(io.Discard, c.stderr)
	close(c.stderrDone)
}

func (c *stdioConn) wait() {
	<-c.outputDone
	<-c.stderrDone
	err := c.cmd.Wait()
	if c.cancel != nil {
		c.cancel()
	}
	if err != nil && c.cmd.ProcessState != nil {
		// Exit diagnostics are intentionally kept out of the Docker byte stream.
	}
	close(c.waitDone)
}

func (c *stdioConn) Close() error {
	var closeErr error
	c.closeOnce.Do(func() {
		c.cancel()
		closeErr = c.Conn.Close()
		_ = c.processEnd.Close()
		_ = c.stdin.Close()
		_ = c.stdout.Close()
		_ = c.stderr.Close()
		select {
		case <-c.waitDone:
		case <-time.After(5 * time.Second):
			if c.cmd.Process != nil {
				_ = c.cmd.Process.Kill()
			}
			select {
			case <-c.waitDone:
			case <-time.After(2 * time.Second):
			}
		}
	})
	return closeErr
}
