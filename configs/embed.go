package configs

import _ "embed"

// ProxyGroups is the reviewed configuration shipped with this release.
//
//go:embed proxy-groups.json
var ProxyGroups []byte
