package main

import (
	"fmt"
	"os"
)

func main() {
	cmd := "run"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	switch cmd {
	case "install":
		cmdInstall(os.Args[2:])
	case "uninstall":
		cmdUninstall()
	case "verify":
		os.Exit(cmdVerify())
	case "status":
		cmdStatus()
	case "run", "":
		run()
	default:
		fmt.Fprintf(os.Stderr, "tailkitd: unknown command %q\n\n", cmd)
		fmt.Fprintf(os.Stderr, "Usage:\n")
		fmt.Fprintf(os.Stderr, "  tailkitd run                  Start the daemon (default)\n")
		fmt.Fprintf(os.Stderr, "  tailkitd install [flags]      Install tailkitd on this node\n")
		fmt.Fprintf(os.Stderr, "  tailkitd uninstall            Remove tailkitd from this node\n")
		fmt.Fprintf(os.Stderr, "  tailkitd verify               Validate installation and config\n")
		fmt.Fprintf(os.Stderr, "  tailkitd status               Show service status\n")
		os.Exit(1)
	}
}
