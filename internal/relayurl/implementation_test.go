package relayurl

import (
	"testing"
)

func TestNew_ValidURLs(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple ws URL",
			input:    "ws://relay.example.com",
			expected: "ws://relay.example.com",
		},
		{
			name:     "simple wss URL",
			input:    "wss://relay.example.com",
			expected: "wss://relay.example.com",
		},
		{
			name:     "ws URL with port",
			input:    "ws://relay.example.com:8080",
			expected: "ws://relay.example.com:8080",
		},
		{
			name:     "wss URL with port",
			input:    "wss://relay.example.com:443",
			expected: "wss://relay.example.com:443",
		},
		{
			name:     "ws URL with path",
			input:    "ws://relay.example.com/path",
			expected: "ws://relay.example.com/path",
		},
		{
			name:     "wss URL with nested path",
			input:    "wss://relay.example.com/nested/path",
			expected: "wss://relay.example.com/nested/path",
		},
		{
			name:     "ws URL with trailing slash",
			input:    "ws://relay.example.com/",
			expected: "ws://relay.example.com",
		},
		{
			name:     "wss URL with trailing slash and path",
			input:    "wss://relay.example.com/path/",
			expected: "wss://relay.example.com/path",
		},
		{
			name:     "uppercase URL",
			input:    "WSS://RELAY.EXAMPLE.COM",
			expected: "wss://relay.example.com",
		},
		{
			name:     "mixed case URL",
			input:    "Ws://ReLaY.ExAmPlE.CoM",
			expected: "ws://relay.example.com",
		},
		{
			name:     "URL with whitespace",
			input:    "  ws://relay.example.com  ",
			expected: "ws://relay.example.com",
		},
		{
			name:     "URL with newline",
			input:    "\nws://relay.example.com\n",
			expected: "ws://relay.example.com",
		},
		{
			name:     "localhost ws",
			input:    "ws://localhost",
			expected: "ws://localhost",
		},
		{
			name:     "localhost wss with port",
			input:    "wss://localhost:8443",
			expected: "wss://localhost:8443",
		},
		{
			name:     "IP address ws",
			input:    "ws://192.168.1.1",
			expected: "ws://192.168.1.1",
		},
		{
			name:     "IP address wss with port",
			input:    "wss://192.168.1.1:443",
			expected: "wss://192.168.1.1:443",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			relay, err := New(tt.input)
			if err != nil {
				t.Fatalf("New() error = %v, want nil", err)
			}
			if relay.String() != tt.expected {
				t.Errorf("String() = %v, want %v", relay.String(), tt.expected)
			}
		})
	}
}

func TestNew_InvalidURLs(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expectedErr string
	}{
		{
			name:        "empty string",
			input:       "",
			expectedErr: "empty relay URL",
		},
		{
			name:        "whitespace only",
			input:       "   ",
			expectedErr: "empty relay URL",
		},
		{
			name:        "http scheme",
			input:       "http://relay.example.com",
			expectedErr: "relay URL must use ws or wss scheme",
		},
		{
			name:        "https scheme",
			input:       "https://relay.example.com",
			expectedErr: "relay URL must use ws or wss scheme",
		},
		{
			name:        "no scheme",
			input:       "relay.example.com",
			expectedErr: "relay URL must use ws or wss scheme",
		},
		{
			name:        "query parameters",
			input:       "ws://relay.example.com?param=value",
			expectedErr: "relay URL must not contain query or fragment",
		},
		{
			name:        "fragment",
			input:       "ws://relay.example.com#fragment",
			expectedErr: "relay URL must not contain query or fragment",
		},
		{
			name:        "query and fragment",
			input:       "ws://relay.example.com?param=value#fragment",
			expectedErr: "relay URL must not contain query or fragment",
		},
		{
			name:        "no host",
			input:       "ws://",
			expectedErr: "relay URL must have a host",
		},
		{
			name:        "no host with path",
			input:       "ws:///path",
			expectedErr: "relay URL must have a host",
		},
		{
			name:        "invalid URL format",
			input:       "://invalid",
			expectedErr: "", // url.Parse will return an error
		},
		{
			name:        "malformed URL",
			input:       "ws://[invalid",
			expectedErr: "", // url.Parse will return an error
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			relay, err := New(tt.input)
			if err == nil {
				t.Errorf("New() error = nil, want error containing %q", tt.expectedErr)
				return
			}
			if relay != nil {
				t.Errorf("New() relay = %v, want nil", relay)
			}
			if tt.expectedErr != "" && err.Error() != tt.expectedErr {
				t.Errorf("New() error = %q, want %q", err.Error(), tt.expectedErr)
			}
		})
	}
}

func TestRelayURL_String(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple URL",
			input:    "ws://relay.example.com",
			expected: "ws://relay.example.com",
		},
		{
			name:     "URL with port",
			input:    "wss://relay.example.com:443",
			expected: "wss://relay.example.com:443",
		},
		{
			name:     "URL with path",
			input:    "ws://relay.example.com/path/to/relay",
			expected: "ws://relay.example.com/path/to/relay",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			relay, err := New(tt.input)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if got := relay.String(); got != tt.expected {
				t.Errorf("String() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestRelayURL_Host(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple host",
			input:    "ws://relay.example.com",
			expected: "relay.example.com",
		},
		{
			name:     "host with port",
			input:    "ws://relay.example.com:8080",
			expected: "relay.example.com",
		},
		{
			name:     "host with path",
			input:    "wss://relay.example.com/path",
			expected: "relay.example.com",
		},
		{
			name:     "localhost",
			input:    "ws://localhost:8080",
			expected: "localhost",
		},
		{
			name:     "IP address",
			input:    "wss://192.168.1.1:443",
			expected: "192.168.1.1",
		},
		{
			name:     "subdomain",
			input:    "ws://sub.relay.example.com",
			expected: "sub.relay.example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			relay, err := New(tt.input)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if got := relay.Host(); got != tt.expected {
				t.Errorf("Host() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestRelayURL_Path(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "no path",
			input:    "ws://relay.example.com",
			expected: "",
		},
		{
			name:     "root path",
			input:    "ws://relay.example.com/",
			expected: "",
		},
		{
			name:     "single path segment",
			input:    "ws://relay.example.com/path",
			expected: "/path",
		},
		{
			name:     "multiple path segments",
			input:    "wss://relay.example.com/nested/path/to/relay",
			expected: "/nested/path/to/relay",
		},
		{
			name:     "path with trailing slash",
			input:    "ws://relay.example.com/path/",
			expected: "/path",
		},
		{
			name:     "path with port",
			input:    "ws://relay.example.com:8080/path",
			expected: "/path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			relay, err := New(tt.input)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if got := relay.Path(); got != tt.expected {
				t.Errorf("Path() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestRelayURL_DTag(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "no path",
			input:    "ws://relay.example.com",
			expected: "ws://relay.example.com/",
		},
		{
			name:     "with path",
			input:    "ws://relay.example.com/path",
			expected: "ws://relay.example.com/path/",
		},
		{
			name:     "with trailing slash in input",
			input:    "ws://relay.example.com/path/",
			expected: "ws://relay.example.com/path/",
		},
		{
			name:     "with port",
			input:    "wss://relay.example.com:443",
			expected: "wss://relay.example.com:443/",
		},
		{
			name:     "with port and path",
			input:    "wss://relay.example.com:443/path",
			expected: "wss://relay.example.com:443/path/",
		},
		{
			name:     "nested path",
			input:    "ws://relay.example.com/nested/path",
			expected: "ws://relay.example.com/nested/path/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			relay, err := New(tt.input)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if got := relay.DTag(); got != tt.expected {
				t.Errorf("DTag() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestRelayURL_Integration(t *testing.T) {
	// Test that all methods work together correctly
	input := "WSS://RELAY.EXAMPLE.COM:443/PATH/TO/RELAY/"
	relay, err := New(input)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// Verify normalization
	if relay.String() != "wss://relay.example.com:443/path/to/relay" {
		t.Errorf("String() = %v, want normalized URL", relay.String())
	}

	// Verify host extraction
	if relay.Host() != "relay.example.com" {
		t.Errorf("Host() = %v, want relay.example.com", relay.Host())
	}

	// Verify path extraction
	if relay.Path() != "/path/to/relay" {
		t.Errorf("Path() = %v, want /path/to/relay", relay.Path())
	}

	// Verify DTag format
	if relay.DTag() != "wss://relay.example.com:443/path/to/relay/" {
		t.Errorf("DTag() = %v, want URL with trailing slash", relay.DTag())
	}
}
