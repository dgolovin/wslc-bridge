package native

import "testing"

func TestDecodeContainerListSupportsArrayAndJSONLines(t *testing.T) {
	tests := []struct {
		name             string
		data             string
		wantFirstPortKey string
		wantHostPort     string
	}{
		{
			name:             "2.9.3 array",
			data:             `[{"Id":"container-a","State":2,"Ports":[{"HostPort":49152,"ContainerPort":80,"Protocol":6,"BindingAddress":"127.0.0.1"}]},{"Id":"container-b","State":3,"Ports":[]}]`,
			wantFirstPortKey: "80/tcp",
			wantHostPort:     "49152",
		},
		{
			name:             "newer JSON lines",
			data:             "{\"ID\":\"container-a\",\"State\":\"running\",\"Ports\":\"127.0.0.1:49152->80/tcp, [::1]:49153->81/udp\"}\n{\"ID\":\"container-b\",\"State\":\"exited\",\"Ports\":\"\"}\n",
			wantFirstPortKey: "80/tcp",
			wantHostPort:     "49152",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			items, err := DecodeContainerList([]byte(test.data))
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != 2 || items[0].ID != "container-a" || items[1].ID != "container-b" {
				t.Fatalf("unexpected list items: %#v", items)
			}
			if items[0].State != "running" || items[0].Ports[test.wantFirstPortKey][0].HostPort != test.wantHostPort {
				t.Fatalf("port mapping was not decoded: %#v", items[0])
			}
			if test.name == "newer JSON lines" {
				binding := items[0].Ports["81/udp"][0]
				if binding.HostIP != "::1" || binding.HostPort != "49153" {
					t.Fatalf("IPv6 formatted mapping was not decoded: %#v", binding)
				}
			}
		})
	}
}
