//go:build gloomi
// +build gloomi

package main

import "embed"

//go:embed schema.sql dist/host dist/gloomi.so
var embeddedAssets embed.FS

var defaultConfig = []byte(`
[[plugins]]
file = "gloomi.so"
domains = ["localhost"]
`)
