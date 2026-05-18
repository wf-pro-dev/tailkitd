package main

import "os"

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		if code, ok := err.(exitCodeError); ok {
			os.Exit(int(code))
		}
		os.Exit(1)
	}
}
