// Package schemas ships the public protocol documentation with the server.
package schemas

import "embed"

// Files contains public protocol definitions, not deployment configuration.
//
//go:embed *.json
var Files embed.FS
