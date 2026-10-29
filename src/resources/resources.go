package resources

import "embed"

//go:embed migrations seed settings
var FS embed.FS
