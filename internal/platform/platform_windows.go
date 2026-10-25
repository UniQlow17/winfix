//go:build windows

package platform

import (
	"winfix/internal/core"
	"winfix/internal/platform/windows"
)

// Current возвращает модуль текущей ОС.
func Current() core.Module { return windows.Module{} }
