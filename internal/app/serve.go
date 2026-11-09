package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"wslc-bridge/internal/buildinfo"
	"wslc-bridge/internal/dockerapi"
	"wslc-bridge/internal/guest"
	"wslc-bridge/internal/native"
	"wslc-bridge/internal/platform"
)

const DefaultPipeName = `\\.\pipe\wslc_engine`

type readinessEvent struct {
	Event           string `json:"event"`
	ProtocolVersion int    `json:"protocolVersion"`
	BridgeVersion   string `json:"bridgeVersion"`
	Pipe            string `json:"pipe"`
	GuestAPIVersion string `json:"guestApiVersion"`
}

func Serve(ctx context.Context, pipeName string) error {
	runtimeInfo, err := DetectRuntime(ctx)
	if err != nil {
		return err
	}
	guestClient := guest.NewClient(runtimeInfo.WSLCPath)
	defer guestClient.CloseIdleConnections()
	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	versionRequest, err := http.NewRequestWithContext(probeCtx, http.MethodGet, "http://docker/version", nil)
	if err != nil {
		return &ExitError{Code: 6, Message: err.Error()}
	}
	versionResponse, err := guestClient.Do(versionRequest)
	if err != nil {
		return &ExitError{Code: 6, Message: "guest Docker API is unavailable: " + err.Error()}
	}
	if versionResponse.StatusCode < 200 || versionResponse.StatusCode >= 300 {
		_ = versionResponse.Body.Close()
		return &ExitError{Code: 6, Message: "guest Docker API returned " + versionResponse.Status}
	}
	var guestVersion struct {
		APIVersion string `json:"ApiVersion"`
	}
	if err := json.NewDecoder(versionResponse.Body).Decode(&guestVersion); err != nil {
		_ = versionResponse.Body.Close()
		return &ExitError{Code: 6, Message: "decode guest Docker version: " + err.Error()}
	}
	_ = versionResponse.Body.Close()
	listener, err := platform.ListenPipe(pipeName)
	if err != nil {
		return &ExitError{Code: 5, Message: err.Error()}
	}
	tracker := newConnectionTracker()
	server := &http.Server{
		Handler:           dockerapi.NewHandler(native.NewClient(runtimeInfo.WSLCPath, runtimeInfo.WSLPath), guestClient),
		ReadHeaderTimeout: 10 * time.Second,
		MaxHeaderBytes:    1 << 20,
		ConnState:         tracker.update,
	}
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.Serve(listener)
	}()
	ready := readinessEvent{
		Event: "ready", ProtocolVersion: 1, BridgeVersion: buildinfo.Version,
		Pipe: pipeName, GuestAPIVersion: guestVersion.APIVersion,
	}
	if err := json.NewEncoder(stdoutWriter{}).Encode(ready); err != nil {
		_ = server.Close()
		return fmt.Errorf("write bridge readiness event: %w", err)
	}
	select {
	case err := <-serveResult:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return &ExitError{Code: 5, Message: "named-pipe server stopped: " + err.Error()}
	case <-ctx.Done():
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer shutdownCancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
		}
		tracker.closeHijacked()
		_ = server.Close()
		select {
		case <-serveResult:
		case <-time.After(2 * time.Second):
		}
		return nil
	}
}

type stdoutWriter struct{}

func (stdoutWriter) Write(data []byte) (int, error) { return fmt.Print(string(data)) }

type connectionTracker struct {
	mu    sync.Mutex
	state map[net.Conn]http.ConnState
}

func newConnectionTracker() *connectionTracker {
	return &connectionTracker{state: make(map[net.Conn]http.ConnState)}
}

func (t *connectionTracker) update(connection net.Conn, state http.ConnState) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if state == http.StateClosed {
		delete(t.state, connection)
		return
	}
	t.state[connection] = state
}

func (t *connectionTracker) closeHijacked() {
	t.mu.Lock()
	connections := make([]net.Conn, 0)
	for connection, state := range t.state {
		if state == http.StateHijacked {
			connections = append(connections, connection)
			delete(t.state, connection)
		}
	}
	t.mu.Unlock()
	for _, connection := range connections {
		_ = connection.Close()
	}
}
