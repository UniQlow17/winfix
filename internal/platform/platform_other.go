//go:build !windows

package platform

import (
	"fmt"
	"os"
	"runtime"

	"winfix/internal/core"
)

// Current возвращает заглушку: для этой ОС модуль ещё не написан.
// Чтобы добавить поддержку, создайте internal/platform/<os> с типом Module
// (core.Module) и подключите его здесь по образцу platform_windows.go.
func Current() core.Module { return stub{} }

type stub struct{}

func (stub) ID() string         { return runtime.GOOS }
func (stub) Title() string      { return runtime.GOOS + " (пока не поддерживается)" }
func (stub) Init(string)        {}
func (stub) Tools() []core.Tool { return nil }
func (stub) IsAdmin() bool      { return os.Geteuid() == 0 }
func (stub) Elevate() bool      { return false }
func (stub) OpenPath(string) error {
	return fmt.Errorf("не поддерживается")
}
