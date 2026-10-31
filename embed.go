// Package omenight holds the node installer so the relay can print it with
// a node's config inside, pinned to the relay's own version.
package omenight

import _ "embed"

//go:embed install.sh
var InstallScript string
