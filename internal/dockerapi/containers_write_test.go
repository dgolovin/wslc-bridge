package dockerapi

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestBuildCreateArgsMapsWindowsBindAndPublishedPorts(t *testing.T) {
	request := dockerCreateRequest{
		Image: "alpine:3.20",
		Cmd:   []string{"sh", "-c", "cat /data/file"},
		HostConfig: dockerHostConfig{
			Binds: []string{`C:\Users\test user\files:/data:ro`},
			PortBindings: map[string][]dockerPortBinding{
				"80/tcp": {{HostIP: "127.0.0.1", HostPort: "0"}},
			},
		},
	}
	args, err := buildCreateArgs(request, "bridge-test")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"container", "create", "--name", "bridge-test",
		"--publish", "127.0.0.1::80/tcp",
		"--volume", `C:\Users\test user\files:/data:ro`,
		"alpine:3.20", "sh", "-c", "cat /data/file",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("buildCreateArgs() = %#v, want %#v", args, want)
	}
}

func TestDecodeCreateRejectsUnknownMeaningfulHostConfig(t *testing.T) {
	var request dockerCreateRequest
	err := json.Unmarshal([]byte(`{"Image":"alpine","HostConfig":{"CgroupNamespace":"host"}}`), &request)
	if err == nil || !strings.Contains(err.Error(), "CgroupNamespace") {
		t.Fatalf("expected unsupported host config error, got %v", err)
	}
}

func TestBuildCreateArgsAcceptsPodmanDesktopStartControlWithoutStartingContainer(t *testing.T) {
	var request dockerCreateRequest
	if err := json.Unmarshal([]byte(`{"Image":"alpine","start":true}`), &request); err != nil {
		t.Fatal(err)
	}
	if !request.Start {
		t.Fatal("expected the Podman Desktop start option to be decoded")
	}
	arguments, err := buildCreateArgs(request, "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(arguments, []string{"container", "create", "alpine"}) {
		t.Fatalf("create request unexpectedly started the container or changed arguments: %#v", arguments)
	}
}

func TestBuildCreateArgsRejectsUnsupportedMeaningfulSemantics(t *testing.T) {
	tests := []dockerCreateRequest{
		{Image: "alpine", Entrypoint: []string{"one", "two"}},
		{Image: "alpine", HostConfig: dockerHostConfig{Privileged: true}},
		{Image: "alpine", HostConfig: dockerHostConfig{RestartPolicy: dockerRestartPolicy{Name: "always"}}},
	}
	for _, request := range tests {
		if _, err := buildCreateArgs(request, ""); err == nil {
			t.Fatalf("buildCreateArgs(%#v) accepted unsupported options", request)
		}
	}
}

func TestBuildCreateArgsReadsMountsFromHostConfig(t *testing.T) {
	request := dockerCreateRequest{
		Image: "alpine",
		HostConfig: dockerHostConfig{
			Mounts: []dockerMount{{Type: "bind", Source: `C:\tmp\input`, Target: "/data", ReadOnly: true}},
		},
	}
	args, err := buildCreateArgs(request, "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(args, []string{"container", "create", "--volume", `C:\tmp\input:/data:ro`, "alpine"}) {
		t.Fatalf("unexpected mapped args: %#v", args)
	}
}

func TestDockerPublishSpecRejectsInvalidAddressAndPort(t *testing.T) {
	if _, err := dockerPublishSpec("80/tcp", dockerPortBinding{HostIP: "not-an-ip", HostPort: "1234"}); err == nil {
		t.Fatal("expected invalid address to fail")
	}
	if _, err := dockerPublishSpec("80/tcp", dockerPortBinding{HostIP: "127.0.0.1", HostPort: "65536"}); err == nil {
		t.Fatal("expected invalid host port to fail")
	}
}

func TestMapBindPreservesDriveColonAndSpaces(t *testing.T) {
	got, err := mapBind(`D:\work dir\src:/app:rw`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, `D:\work dir\src:/app:`) {
		t.Fatalf("mapBind() = %q, expected Windows drive path intact", got)
	}
}
