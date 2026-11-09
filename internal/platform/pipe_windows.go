//go:build windows

package platform

import (
	"fmt"
	"net"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func ListenPipe(name string) (net.Listener, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return nil, fmt.Errorf("open current Windows process token: %w", err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("get current Windows user SID: %w", err)
	}
	sid := user.User.Sid.String()
	securityDescriptor := fmt.Sprintf("D:P(A;;GA;;;%s)", sid)
	listener, err := winio.ListenPipe(name, &winio.PipeConfig{
		SecurityDescriptor: securityDescriptor,
		MessageMode:        false,
	})
	if err != nil {
		return nil, fmt.Errorf("listen on named pipe %q: %w", name, err)
	}
	return listener, nil
}
