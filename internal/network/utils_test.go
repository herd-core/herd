package network

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeNetworkBindings(t *testing.T) {
	tests := []struct {
		input    string
		binding  string
		protocol string
		err      bool
	}{
		{
			input:    ":80",
			binding:  "0.0.0.0:0:80",
			protocol: "tcp",
			err:      false,
		},
		{
			input:    "8080:80",
			binding:  "0.0.0.0:8080:80",
			protocol: "tcp",
			err:      false,
		},
		{
			input:    "0:80",
			binding:  "0.0.0.0:0:80",
			protocol: "tcp",
			err:      false,
		},
	}
	for _, test := range tests {

		bindings, protocol, err := SanitizeNetworkBindings(test.input)
		assert.Equal(t, test.binding, bindings)
		assert.Equal(t, test.protocol, protocol)
		if test.err {
			assert.Error(t, err)
		} else {
			assert.NoError(t, err)
		}
	}
}
