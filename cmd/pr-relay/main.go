// Command pr-relay watches GitHub pull requests and pushes filtered updates
// into herdr panes.
package main

import "fmt"

// version is overridden at release time via -ldflags "-X main.version=...".
var version = "dev"

func versionString() string {
	return "pr-relay " + version
}

func main() {
	fmt.Println(versionString())
}
