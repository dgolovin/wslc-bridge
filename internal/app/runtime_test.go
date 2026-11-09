package app

import (
	"encoding/binary"
	"testing"

	"wslc-bridge/internal/wslutil"
)

func TestParseWSLVersion(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
		wantOK bool
	}{
		{name: "minimum prerelease", output: "WSL version: 2.9.3.0\nKernel version: 6.6.87", want: "2.9.3", wantOK: true},
		{name: "newer", output: "WSL version: 3.0.3.0", want: "3.0.3", wantOK: true},
		{name: "no version label", output: "kernel version: 6.6.87", wantOK: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := parseWSLVersion(test.output)
			if got != test.want || ok != test.wantOK {
				t.Fatalf("parseWSLVersion(%q) = (%q, %t), want (%q, %t)", test.output, got, ok, test.want, test.wantOK)
			}
		})
	}
}

func TestCompareVersionUsesNumericComponents(t *testing.T) {
	if compareVersion("2.10.0", "2.9.3") <= 0 {
		t.Fatal("2.10.0 must compare newer than 2.9.3")
	}
	if compareVersion("2.9.2", MinimumWSLVersion) >= 0 {
		t.Fatal("2.9.2 must compare older than the minimum")
	}
}

func TestDecodeWindowsOutputUTF16LE(t *testing.T) {
	value := "WSL version: 2.9.3.0"
	data := make([]byte, 2+len(value)*2)
	data[0], data[1] = 0xff, 0xfe
	for i, character := range value {
		binary.LittleEndian.PutUint16(data[2+i*2:], uint16(character))
	}
	if got := wslutil.DecodeWindowsOutput(data); got != value {
		t.Fatalf("DecodeWindowsOutput() = %q, want %q", got, value)
	}
}
