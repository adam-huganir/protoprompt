package store

import "crypto/rand"

// NewID returns 8 random bytes hex-encoded (16 chars).
func NewID() string {
	var b [8]byte
	for {
		if _, err := rand.Read(b[:]); err == nil {
			break
		}
	}
	const hex = "0123456789abcdef"
	out := make([]byte, 16)
	for i, v := range b {
		out[i*2] = hex[v>>4]
		out[i*2+1] = hex[v&0xf]
	}
	return string(out)
}
