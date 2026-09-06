// Package contracts embeds the contract-first HTTP document shared with clients.
package contracts

import _ "embed"

//go:embed openapi.json
var OpenAPI []byte
