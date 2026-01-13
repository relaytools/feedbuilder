package relayurl

// Represents a validated relay URL.
type RelayURL interface {
	// Returns the canonical string form of the relay URL.
	String() string

	// Returns the lowercase host without port, for host-based grouping.
	Host() string

	// Returns relative path
	Path() string

	// DTag returns the URL in the form expected for NIP-66 d-tags
	// (normalized plus a trailing slash).
	DTag() string
}

