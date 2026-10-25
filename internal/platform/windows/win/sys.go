//go:build windows

package win

import (
	"os"
	"strings"
	"syscall"
	"unsafe"

	"winfix/internal/core"
	"winfix/internal/ui"
)

var (
	shell32           = syscall.NewLazyDLL("shell32.dll")
	psapi             = syscall.NewLazyDLL("psapi.dll")
	procIsUserAnAdmin = shell32.NewProc("IsUserAnAdmin")
	procShellExecute  = shell32.NewProc("ShellExecuteW")
	procGlobalMemory  = kernel32.NewProc("GlobalMemoryStatusEx")
	procGetPerfInfo   = psapi.NewProc("GetPerformanceInfo")
)

// IsAdmin сообщает, запущен ли процесс с правами администратора.
func IsAdmin() bool {
	r, _, _ := procIsUserAnAdmin.Call()
	return r != 0
}

// Elevate перезапускает текущую программу с запросом UAC. true — запрос ушёл.
func Elevate() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	args := make([]string, 0, len(os.Args))
	for _, a := range os.Args[1:] {
		args = append(args, syscall.EscapeArg(a))
	}
	args = append(args, core.ElevatedFlag)
	verb, _ := syscall.UTF16PtrFromString("runas")
	file, _ := syscall.UTF16PtrFromString(exe)
	params, _ := syscall.UTF16PtrFromString(strings.Join(args, " "))
	r, _, _ := procShellExecute.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(file)),
		uintptr(unsafe.Pointer(params)), 0, 1)
	return r > 32
}

// RequireAdmin при необходимости перезапускает программу от администратора
// (старый процесс завершается). Если UAC отклонён — работаем дальше без прав.
func RequireAdmin(reason string) {
	if IsAdmin() {
		return
	}
	ui.Warn("Нужны права администратора (%s). Запрашиваю UAC…", reason)
	if Elevate() {
		os.Exit(0)
	}
	ui.Fail("Права администратора не получены — часть функций будет недоступна.")
}

// MemStatus — сведения о памяти (GlobalMemoryStatusEx).
type MemStatus struct {
	Load                         uint32
	TotalPhys, AvailPhys         uint64
	TotalPageFile, AvailPageFile uint64
}

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

// Memory возвращает текущее состояние памяти.
func Memory() MemStatus {
	m := memoryStatusEx{Length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	procGlobalMemory.Call(uintptr(unsafe.Pointer(&m)))
	return MemStatus{m.MemoryLoad, m.TotalPhys, m.AvailPhys, m.TotalPageFile, m.AvailPageFile}
}

// PerfInfo — данные GetPerformanceInfo (в байтах).
type PerfInfo struct {
	CommitTotal, CommitLimit, CommitPeak uint64
	PhysTotal, PhysAvail, SystemCache    uint64
	KernelPaged, KernelNonpaged          uint64
	Handles, Processes, Threads          uint32
}

type performanceInformation struct {
	Cb                                   uint32
	CommitTotal, CommitLimit, CommitPeak uintptr
	PhysicalTotal, PhysicalAvailable     uintptr
	SystemCache                          uintptr
	KernelTotal, KernelPaged             uintptr
	KernelNonpaged, PageSize             uintptr
	HandleCount, ProcessCount, ThreadCnt uint32
}

// Performance возвращает счётчики commit/кэша/пула ядра.
func Performance() PerfInfo {
	p := performanceInformation{Cb: uint32(unsafe.Sizeof(performanceInformation{}))}
	if r, _, _ := procGetPerfInfo.Call(uintptr(unsafe.Pointer(&p)), uintptr(p.Cb)); r == 0 {
		return PerfInfo{}
	}
	ps := uint64(p.PageSize)
	return PerfInfo{
		CommitTotal: uint64(p.CommitTotal) * ps, CommitLimit: uint64(p.CommitLimit) * ps, CommitPeak: uint64(p.CommitPeak) * ps,
		PhysTotal: uint64(p.PhysicalTotal) * ps, PhysAvail: uint64(p.PhysicalAvailable) * ps,
		SystemCache: uint64(p.SystemCache) * ps, KernelPaged: uint64(p.KernelPaged) * ps, KernelNonpaged: uint64(p.KernelNonpaged) * ps,
		Handles: p.HandleCount, Processes: p.ProcessCount, Threads: p.ThreadCnt,
	}
}
