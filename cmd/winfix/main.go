// winfix — единая программа обслуживания компьютера: меню и командная строка.
package main

import (
	"os"

	"winfix/internal/core"
	"winfix/internal/platform"
)

// version подставляется при сборке: -ldflags "-X main.version=1.0.0".
var version = "dev"

func main() {
	app := &core.App{Mod: platform.Current(), Version: version}
	os.Exit(app.Run(os.Args[1:]))
}
