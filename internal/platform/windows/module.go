//go:build windows

// Package windows — модуль WinFix для Windows: набор утилит и всё,
// что зависит от системы (права администратора, консоль, PowerShell, установка).
package windows

import (
	"os/exec"

	"winfix/internal/core"
	"winfix/internal/platform/windows/drivercheck"
	"winfix/internal/platform/windows/healthcheck"
	"winfix/internal/platform/windows/junkclean"
	"winfix/internal/platform/windows/memcheck"
	"winfix/internal/platform/windows/netfix"
	"winfix/internal/platform/windows/sysrepair"
	"winfix/internal/platform/windows/win"
)

// Module — реализация core.Module и core.Installer для Windows.
type Module struct{}

func (Module) ID() string    { return "windows" }
func (Module) Title() string { return "Windows" }

func (Module) Init(title string) { win.InitConsole(title) }

func (Module) Tools() []core.Tool {
	return []core.Tool{
		healthcheck.Tool(),
		junkclean.Tool(),
		drivercheck.Tool(),
		sysrepair.Tool(),
		memcheck.Tool(),
		netfix.Tool(),
	}
}

func (Module) IsAdmin() bool { return win.IsAdmin() }
func (Module) Elevate() bool { return win.Elevate() }

func (Module) OpenPath(dir string) error { return exec.Command("explorer.exe", dir).Start() }
