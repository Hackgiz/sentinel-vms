// Package api holds the OpenAPI document for /api/v2 (openapi.yaml), the
// contract the server implements and the clients are generated from.
package api

import _ "embed"

//go:embed openapi.yaml
var OpenAPI []byte
