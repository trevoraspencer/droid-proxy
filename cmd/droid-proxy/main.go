// Command droid-proxy is a localhost HTTP proxy that lets Factory Droid use
// any BYOK / custom model through a single Go binary.
package main

import (
	"os"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "auth":
			runAuth(os.Args[2:])
			return
		case "start":
			runStart(os.Args[2:])
			return
		case "stop":
			runStop()
			return
		case "status":
			runStatus()
			return
		case "restart":
			runRestart(os.Args[2:])
			return
		case "service":
			runService(os.Args[2:])
			return
		case "logs":
			runLogs(os.Args[2:])
			return
		case "config", "onboard":
			runConfig(os.Args[2:])
			return
		case "update":
			runUpdate(os.Args[2:])
			return
		}
	}

	runServerCLI(os.Args[1:])
}
