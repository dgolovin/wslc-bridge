//go:build !windows

package platform

import (
	"fmt"
	"net"
)

func ListenPipe(name string) (net.Listener, error) {
	return nil, fmt.Errorf("the named-pipe bridge only runs on Windows (requested %q)", name)
}
