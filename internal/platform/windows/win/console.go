//go:build windows

// Package win — низкоуровневые средства Windows, общие для утилит модуля:
// консоль, права администратора, запуск программ и PowerShell, сведения о памяти.
package win

import (
	"os"
	"syscall"
	"unsafe"
	"winfix/internal/ui"
)

var (
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procSetConsoleOutputCP = kernel32.NewProc("SetConsoleOutputCP")
	procSetConsoleCP       = kernel32.NewProc("SetConsoleCP")
	procGetConsoleMode     = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode     = kernel32.NewProc("SetConsoleMode")
	procSetConsoleTitle    = kernel32.NewProc("SetConsoleTitleW")
	procGetLogicalDrives   = kernel32.NewProc("GetLogicalDrives")
	procGetDriveType       = kernel32.NewProc("GetDriveTypeW")
)

// InitConsole включает UTF-8, цвета ANSI и задаёт заголовок окна.
func InitConsole(title string) {
	vt := true
	procSetConsoleOutputCP.Call(65001)
	procSetConsoleCP.Call(65001)
	if h, err := syscall.GetStdHandle(syscall.STD_OUTPUT_HANDLE); err == nil {
		var mode uint32
		if r, _, _ := procGetConsoleMode.Call(uintptr(h), uintptr(unsafe.Pointer(&mode))); r != 0 {
			r, _, _ = procSetConsoleMode.Call(uintptr(h), uintptr(mode|0x0004|0x0001))
			vt = r != 0
		}
	}
	if p, err := syscall.UTF16PtrFromString(title); err == nil {
		procSetConsoleTitle.Call(uintptr(unsafe.Pointer(p)))
	}
	ui.SetTerminal(vt)
	if len(os.Args) <= 2 { // интерактивный запуск: меню должно поместиться целиком
		enlargeConsole(110, 42)
	}
}

type coord struct{ X, Y int16 }
type smallRect struct{ Left, Top, Right, Bottom int16 }
type screenBufferInfo struct {
	Size              coord
	CursorPosition    coord
	Attributes        uint16
	Window            smallRect
	MaximumWindowSize coord
}

var (
	procGetScreenBufferInfo  = kernel32.NewProc("GetConsoleScreenBufferInfo")
	procSetScreenBufferSize  = kernel32.NewProc("SetConsoleScreenBufferSize")
	procSetConsoleWindowInfo = kernel32.NewProc("SetConsoleWindowInfo")
)

// enlargeConsole увеличивает окно классической консоли (conhost) до cols×rows,
// насколько позволяет экран. Windows Terminal размер окна программно не меняется —
// там меню само переходит в компактный вид.
func enlargeConsole(cols, rows int16) {
	if os.Getenv("WT_SESSION") != "" {
		return
	}
	h, err := syscall.GetStdHandle(syscall.STD_OUTPUT_HANDLE)
	if err != nil {
		return
	}
	var info screenBufferInfo
	if r, _, _ := procGetScreenBufferInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&info))); r == 0 {
		return
	}
	if cols > info.MaximumWindowSize.X {
		cols = info.MaximumWindowSize.X
	}
	if rows > info.MaximumWindowSize.Y {
		rows = info.MaximumWindowSize.Y
	}
	curCols := info.Window.Right - info.Window.Left + 1
	curRows := info.Window.Bottom - info.Window.Top + 1
	if cols < curCols {
		cols = curCols
	}
	if rows < curRows {
		rows = curRows
	}
	// сначала буфер (он не может быть меньше окна), потом окно
	size := coord{X: max(cols, info.Size.X), Y: max(rows, info.Size.Y)}
	packed := uintptr(uint32(uint16(size.X)) | uint32(uint16(size.Y))<<16)
	procSetScreenBufferSize.Call(uintptr(h), packed)
	rect := smallRect{Left: 0, Top: 0, Right: cols - 1, Bottom: rows - 1}
	procSetConsoleWindowInfo.Call(uintptr(h), 1, uintptr(unsafe.Pointer(&rect)))
}

// FixedDrives возвращает буквы локальных (несъёмных) дисков, например ["C:", "D:"].
func FixedDrives() []string {
	mask, _, _ := procGetLogicalDrives.Call()
	var res []string
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		p, _ := syscall.UTF16PtrFromString(root)
		if t, _, _ := procGetDriveType.Call(uintptr(unsafe.Pointer(p))); t == 3 { // DRIVE_FIXED
			res = append(res, root[:2])
		}
	}
	return res
}
