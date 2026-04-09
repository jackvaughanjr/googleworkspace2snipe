package main

import "github.com/jackvaughanjr/googleworkspace2snipe/cmd"

// version is set at build time via -ldflags "-X main.version=vX.Y.Z"
var version = "dev"

func main() {
	cmd.SetVersion(version)
	cmd.Execute()
}
