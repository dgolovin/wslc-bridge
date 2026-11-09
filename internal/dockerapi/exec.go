package dockerapi

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"wslc-bridge/internal/execstore"
	"wslc-bridge/internal/process"
)

type execCreateRequest struct {
	AttachStdin  bool     `json:"AttachStdin"`
	AttachStdout bool     `json:"AttachStdout"`
	AttachStderr bool     `json:"AttachStderr"`
	Tty          bool     `json:"Tty"`
	Cmd          []string `json:"Cmd"`
	Env          []string `json:"Env"`
	WorkingDir   string   `json:"WorkingDir"`
	User         string   `json:"User"`
}

type execStartRequest struct {
	Detach bool `json:"Detach"`
	Tty    bool `json:"Tty"`
}

func (h *Handler) createExec(w http.ResponseWriter, r *http.Request, path string) {
	containerID := strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/exec")
	var request execCreateRequest
	if err := decodeRequest(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(request.Cmd) == 0 || strings.TrimSpace(request.Cmd[0]) == "" {
		writeError(w, http.StatusBadRequest, "Cmd must contain at least one executable")
		return
	}
	if request.AttachStdin || request.Tty || len(request.Env) > 0 || request.WorkingDir != "" || request.User != "" {
		writeError(w, http.StatusNotImplemented, "interactive exec and exec environment/user/working-directory overrides are not supported")
		return
	}
	if !request.AttachStdout && !request.AttachStderr {
		writeError(w, http.StatusBadRequest, "AttachStdout or AttachStderr must be enabled for non-interactive exec")
		return
	}
	running, status, err := h.guestContainerRunning(r, containerID)
	if err != nil {
		writeError(w, status, "guest Docker container inspect failed: "+err.Error())
		return
	}
	if !running {
		writeError(w, http.StatusConflict, "container must be running before creating an exec instance")
		return
	}
	record, err := h.execs.Create(containerID, request.Cmd)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"Id": record.ID})
}

func (h *Handler) startExec(w http.ResponseWriter, r *http.Request, path string) {
	id := execIDFromPath(path, "start")
	record, ok := h.execs.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "exec instance not found")
		return
	}
	var request execStartRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := decodeRequest(r, &request); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if request.Detach || request.Tty {
		writeError(w, http.StatusNotImplemented, "detached and TTY exec are not supported")
		return
	}
	record, err := h.execs.Start(id)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		h.execs.Fail(id)
		writeError(w, http.StatusInternalServerError, "HTTP connection does not support Docker exec streaming")
		return
	}
	connection, buffered, err := hijacker.Hijack()
	if err != nil {
		h.execs.Fail(id)
		return
	}
	defer connection.Close()
	if _, err := buffered.WriteString("HTTP/1.1 101 UPGRADED\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n"); err != nil {
		h.execs.Fail(id)
		return
	}
	if err := buffered.Flush(); err != nil {
		h.execs.Fail(id)
		return
	}
	stream := &dockerStreamWriter{writer: connection}
	commandCtx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	stdout := io.Writer(streamForType(stream, 1))
	stderr := io.Writer(streamForType(stream, 2))
	exitCode, runErr := h.native.ExecContainer(commandCtx, record.ContainerID, record.Cmd, stdout, stderr)
	if runErr != nil {
		var processCommandError *process.CommandError
		if !errors.As(runErr, &processCommandError) || processCommandError.ExitCode < 0 {
			h.execs.Fail(id)
			return
		}
		exitCode = processCommandError.ExitCode
	}
	if err := buffered.Flush(); err != nil {
		h.execs.Fail(id)
		return
	}
	_, _ = h.execs.Complete(id, exitCode)
}

func (h *Handler) inspectExec(w http.ResponseWriter, _ *http.Request, path string) {
	id := execIDFromPath(path, "json")
	record, ok := h.execs.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "exec instance not found")
		return
	}
	response := map[string]any{
		"ID":            record.ID,
		"ContainerID":   record.ContainerID,
		"Running":       record.State == execstore.Running,
		"ExitCode":      record.ExitCode,
		"ProcessConfig": map[string]any{"entrypoint": firstCommand(record.Cmd), "arguments": remainingCommand(record.Cmd), "tty": false, "privileged": false, "user": ""},
	}
	writeJSON(w, http.StatusOK, response)
}

func firstCommand(command []string) string {
	if len(command) == 0 {
		return ""
	}
	return command[0]
}

func remainingCommand(command []string) []string {
	if len(command) < 2 {
		return []string{}
	}
	return command[1:]
}

type dockerStreamWriter struct {
	writer io.Writer
	mu     sync.Mutex
}

type typedStreamWriter struct {
	parent *dockerStreamWriter
	stream byte
}

func streamForType(parent *dockerStreamWriter, stream byte) io.Writer {
	return typedStreamWriter{parent: parent, stream: stream}
}

func (w typedStreamWriter) Write(value []byte) (int, error) {
	w.parent.mu.Lock()
	defer w.parent.mu.Unlock()
	total := len(value)
	for len(value) > 0 {
		length := len(value)
		if length > 32*1024 {
			length = 32 * 1024
		}
		var header [8]byte
		header[0] = w.stream
		binary.BigEndian.PutUint32(header[4:], uint32(length))
		if _, err := w.parent.writer.Write(header[:]); err != nil {
			return 0, err
		}
		written, err := w.parent.writer.Write(value[:length])
		if err != nil {
			return 0, err
		}
		if written != length {
			return 0, io.ErrShortWrite
		}
		value = value[length:]
	}
	return total, nil
}
