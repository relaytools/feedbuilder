package relayurl

import (
	"errors"
	"net/url"
	"strings"
)

type relayUrl struct {
	u *url.URL
}

// New parses and validates a relay URL string.
// It trims whitespace, lowercases, removes a trailing slash, and enforces:
//   - ws:// or wss:// scheme
//   - no query parameters or fragments
//   - non-empty host
func New(raw string) (RelayURL, error) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	raw = strings.TrimSuffix(raw, "/")
	if raw == "" {
		return nil, errors.New("empty relay URL")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}

	if u.Scheme != "ws" && u.Scheme != "wss" {
		return nil, errors.New("relay URL must use ws or wss scheme")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("relay URL must not contain query or fragment")
	}
	if u.Host == "" {
		return nil, errors.New("relay URL must have a host")
	}

	return relayUrl{ u: u }, nil
}

func (r relayUrl) String() string {
	return r.u.String()
}

func (r relayUrl) Host() string {
	return r.u.Hostname()
}

func (r relayUrl) Path() string {
	return r.u.Path
}

func (r relayUrl) DTag() string {
	s := r.String()
	if !strings.HasSuffix(s, "/") {
		s += "/"
	}
	return s
}


