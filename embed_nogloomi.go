//go:build !gloomi
// +build !gloomi

package main

import "embed"

//go:embed schema.sql dist/host
var embeddedAssets embed.FS

var defaultConfig = []byte("# no plugins to preload in this build\n")
