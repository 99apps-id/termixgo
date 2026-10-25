//go:build !windows

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "TermixgoSetup is only supported on Windows. On Linux and macOS, use install.sh or run termixgo directly.")
	os.Exit(1)
}
