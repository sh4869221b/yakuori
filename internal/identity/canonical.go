// Package identity defines versioned canonical identities independently of adapters.
package identity

import "encoding/hex"

const SourceVersion = 1

func digestHex(digest [32]byte) string { return hex.EncodeToString(digest[:]) }
