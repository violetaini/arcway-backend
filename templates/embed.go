package templates

import "embed"

//go:embed all:*
var fs embed.FS

func ReadFile(name string) ([]byte, error) {
	return fs.ReadFile(name)
}
