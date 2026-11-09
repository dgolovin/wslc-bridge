package overlay

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"wslc-bridge/internal/native"
)

type ContainerPorts struct {
	Ports   map[string][]native.PortBinding
	Running bool
}

func Inspect(guestBody []byte, nativePorts map[string][]native.PortBinding) ([]byte, error) {
	var container map[string]any
	decoder := json.NewDecoder(bytes.NewReader(guestBody))
	decoder.UseNumber()
	if err := decoder.Decode(&container); err != nil {
		return nil, fmt.Errorf("decode guest container inspect: %w", err)
	}
	if container == nil {
		return nil, errors.New("guest container inspect is not a JSON object")
	}
	networkSettings, ok := container["NetworkSettings"].(map[string]any)
	if !ok {
		networkSettings = make(map[string]any)
		container["NetworkSettings"] = networkSettings
	}
	guestPorts, ok := networkSettings["Ports"].(map[string]any)
	if !ok {
		guestPorts = make(map[string]any)
	}
	// Keep declared ports but discard any guest-VM host port: it is not the
	// Windows host port exposed by WSLC's native relay.
	for key, value := range guestPorts {
		if value == nil {
			continue
		}
		bindings, ok := value.([]any)
		if !ok {
			continue
		}
		for _, bindingValue := range bindings {
			binding, ok := bindingValue.(map[string]any)
			if !ok {
				continue
			}
			binding["HostIp"] = ""
			binding["HostPort"] = ""
		}
		guestPorts[key] = bindings
	}
	for port, bindings := range nativePorts {
		converted := make([]any, 0, len(bindings))
		for _, binding := range bindings {
			converted = append(converted, map[string]any{"HostIp": binding.HostIP, "HostPort": binding.HostPort})
		}
		guestPorts[port] = converted
	}
	networkSettings["Ports"] = guestPorts
	return json.Marshal(container)
}

func Summary(guestBody []byte, nativeByID map[string]ContainerPorts) ([]byte, error) {
	var containers []map[string]any
	decoder := json.NewDecoder(bytes.NewReader(guestBody))
	decoder.UseNumber()
	if err := decoder.Decode(&containers); err != nil {
		return nil, fmt.Errorf("decode guest container list: %w", err)
	}
	for _, container := range containers {
		id, _ := container["Id"].(string)
		if id == "" {
			id, _ = container["ID"].(string)
		}
		nativeContainer, found := nativeByID[id]
		if !found {
			continue
		}
		existing, _ := container["Ports"].([]any)
		portRows := make([]any, 0, len(existing)+len(nativeContainer.Ports))
		for _, row := range existing {
			value, ok := row.(map[string]any)
			if !ok {
				continue
			}
			privatePort := parseNumber(value["PrivatePort"])
			protocol := stringValue(value["Type"])
			if protocol == "" {
				protocol = "tcp"
			}
			key := fmt.Sprintf("%d/%s", privatePort, protocol)
			bindings := nativeContainer.Ports[key]
			bindingIndex := -1
			if nativeContainer.Running {
				for i, binding := range bindings {
					if bindingIndex < 0 || binding.HostIP == stringValue(value["IP"]) {
						bindingIndex = i
						if binding.HostIP == stringValue(value["IP"]) {
							break
						}
					}
				}
			}
			if bindingIndex >= 0 {
				binding := bindings[bindingIndex]
				value["PublicPort"] = json.Number(binding.HostPort)
				value["IP"] = binding.HostIP
				bindings = append(bindings[:bindingIndex], bindings[bindingIndex+1:]...)
				nativeContainer.Ports[key] = bindings
			} else if parseNumber(value["PublicPort"]) != 0 {
				value["PublicPort"] = json.Number("0")
				value["IP"] = ""
			}
			portRows = append(portRows, value)
		}
		if nativeContainer.Running {
			for key, bindings := range nativeContainer.Ports {
				privatePort, protocol, ok := parsePortKey(key)
				if !ok {
					continue
				}
				for _, binding := range bindings {
					publicPort, err := strconv.Atoi(binding.HostPort)
					if err != nil {
						return nil, fmt.Errorf("invalid native Windows host port %q for %s", binding.HostPort, key)
					}
					portRows = append(portRows, map[string]any{
						"PrivatePort": json.Number(strconv.Itoa(privatePort)),
						"PublicPort":  json.Number(strconv.Itoa(publicPort)),
						"Type":        protocol,
						"IP":          binding.HostIP,
					})
				}
			}
		}
		container["Ports"] = portRows
	}
	return json.Marshal(containers)
}

func parsePortKey(value string) (int, string, bool) {
	parts := strings.SplitN(value, "/", 2)
	if len(parts) != 2 || (parts[1] != "tcp" && parts[1] != "udp") {
		return 0, "", false
	}
	port, err := strconv.Atoi(parts[0])
	return port, parts[1], err == nil && port > 0 && port <= 65535
}

func parseNumber(value any) int {
	switch number := value.(type) {
	case json.Number:
		parsed, _ := number.Int64()
		return int(parsed)
	case float64:
		return int(number)
	case string:
		parsed, _ := strconv.Atoi(number)
		return parsed
	default:
		return 0
	}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
