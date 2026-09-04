package main

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeNetworkBindingsV2(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected portBinding
		err      bool
	}{
		// valid inputs
		{
			name:  "Empty host port",
			input: ":80",
			expected: portBinding{
				IP:        netip.MustParseAddr("0.0.0.0"),
				HostPort:  0,
				GuestPort: 80,
				Protocol:  "tcp",
			},
			err: false,
		},
		{
			name:  "Host port and guest port",
			input: "8080:80",
			expected: portBinding{
				IP:        netip.MustParseAddr("0.0.0.0"),
				HostPort:  8080,
				GuestPort: 80,
				Protocol:  "tcp",
			},
			err: false,
		},
		{
			name:  "Host port 0 and guest port",
			input: "0:80",
			expected: portBinding{
				IP:        netip.MustParseAddr("0.0.0.0"),
				HostPort:  0,
				GuestPort: 80,
				Protocol:  "tcp",
			},
			err: false,
		},
		{
			name:  "IP, host port 0, and guest port",
			input: "192.168.0.3:0:80",
			expected: portBinding{
				IP:        netip.MustParseAddr("192.168.0.3"),
				HostPort:  0,
				GuestPort: 80,
				Protocol:  "tcp",
			},
			err: false,
		},
		{
			name:  "Default IP, host port 0, and guest port",
			input: "0.0.0.0:0:80",
			expected: portBinding{
				IP:        netip.MustParseAddr("0.0.0.0"),
				HostPort:  0,
				GuestPort: 80,
				Protocol:  "tcp",
			},
			err: false,
		},
		{
			name:  "Guest port only",
			input: "80", // V2 handles this correctly now!
			expected: portBinding{
				IP:        netip.MustParseAddr("0.0.0.0"),
				HostPort:  0,
				GuestPort: 80,
				Protocol:  "tcp",
			},
			err: false,
		},
		{
			name:  "With udp protocol",
			input: "8080:80/udp",
			expected: portBinding{
				IP:        netip.MustParseAddr("0.0.0.0"),
				HostPort:  8080,
				GuestPort: 80,
				Protocol:  "udp",
			},
			err: false,
		},
		{
			name:  "Empty IP and host port",
			input: "::80", // V2 successfully parses this as empty defaults
			expected: portBinding{
				IP:        netip.MustParseAddr("0.0.0.0"),
				HostPort:  0,
				GuestPort: 80,
				Protocol:  "tcp",
			},
			err: false,
		},
		{
			name:  "Empty IP and host port with tcp",
			input: "::80/tcp",
			expected: portBinding{
				IP:        netip.MustParseAddr("0.0.0.0"),
				HostPort:  0,
				GuestPort: 80,
				Protocol:  "tcp",
			},
			err: false,
		},
		// invalid inputs
		{
			name:     "Invalid guest port",
			input:    "0.0.0.0:0:99999",
			expected: portBinding{},
			err:      true,
		},
		{
			name:     "Too many colons",
			input:    "127.0.0.1:0:0:80",
			expected: portBinding{},
			err:      true,
		},
		{
			name:     "Invalid IP",
			input:    "192.168.0.256:0:80/tcp",
			expected: portBinding{},
			err:      true,
		},
		{
			name:     "Invalid Protocol",
			input:    "8080:80/invalid",
			expected: portBinding{},
			err:      true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			binding, err := sanitizeNetworkBindings(test.input)
			if test.err {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, test.expected, binding)
			}
		})
	}
}
