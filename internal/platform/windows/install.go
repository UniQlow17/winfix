//go:build windows

package windows

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"winfix/internal/platform/windows/win"
	"winfix/internal/ui"
)

// installDir — папка установки: %LOCALAPPDATA%\Programs\WinFix (права администратора не нужны).
func installDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base, _ = os.UserCacheDir()
	}
	return filepath.Join(base, "Programs", "WinFix")
}

func psq(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// Общая часть скриптов: ярлыки на рабочем столе и в меню «Пуск» с запуском
// от администратора (бит SLDF_RUNAS_USER в файле ярлыка).
const shortcutFn = `
function Set-WinFixShortcut($lnk, $exe, $dir) {
  $ws = New-Object -ComObject WScript.Shell
  $s = $ws.CreateShortcut($lnk)
  $s.TargetPath = $exe; $s.WorkingDirectory = $dir; $s.IconLocation = "$exe,0"
  $s.Description = 'WinFix: диагностика и обслуживание Windows'
  $s.Save()
  $b = [IO.File]::ReadAllBytes($lnk); $b[0x15] = $b[0x15] -bor 0x20; [IO.File]::WriteAllBytes($lnk, $b)
}
$places = @([Environment]::GetFolderPath('Desktop'), [Environment]::GetFolderPath('Programs'))
`

// Install копирует программу в профиль пользователя, добавляет её в PATH
// и создаёт ярлыки с запуском от администратора.
func (Module) Install() error {
	src, err := os.Executable()
	if err != nil {
		return err
	}
	dir := installDir()
	dst := filepath.Join(dir, "winfix.exe")
	ui.Header("Установка WinFix")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if !strings.EqualFold(src, dst) {
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("не удалось скопировать программу в %s: %w", dir, err)
		}
	}
	ui.OK("Программа: %s", dst)

	script := shortcutFn + fmt.Sprintf(`
$dir = %s; $exe = %s
$k = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)
$p = [string]$k.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
if (($p -split ';') -notcontains $dir) {
  $k.SetValue('Path', ($p.TrimEnd(';') + ';' + $dir).TrimStart(';'), [Microsoft.Win32.RegistryValueKind]::ExpandString)
  'PATH обновлён'
}
$k.Close()
foreach ($pl in $places) { Set-WinFixShortcut (Join-Path $pl 'WinFix.lnk') $exe $dir }
'ярлыки созданы'`, psq(dir), psq(dst))
	out, err := win.PS(time.Minute, script)
	if err != nil {
		return fmt.Errorf("не удалось настроить PATH и ярлыки: %w", err)
	}
	ui.Dim("%s", strings.ReplaceAll(out, "\n", "; "))
	ui.OK("Готово. Ярлык «WinFix» — на рабочем столе и в меню «Пуск» (запуск от администратора).")
	ui.Info("Команда winfix станет доступна в новых окнах терминала.")
	return nil
}

// Uninstall удаляет ярлыки, запись в PATH и установленную программу.
func (Module) Uninstall() error {
	dir := installDir()
	ui.Header("Удаление WinFix")
	script := shortcutFn + fmt.Sprintf(`
$dir = %s
$k = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)
$p = [string]$k.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
$new = (($p -split ';') | Where-Object { $_ -and $_ -ne $dir }) -join ';'
if ($new -ne $p) { $k.SetValue('Path', $new, [Microsoft.Win32.RegistryValueKind]::ExpandString); 'PATH очищен' }
$k.Close()
foreach ($pl in $places) { Remove-Item (Join-Path $pl 'WinFix.lnk') -Force -ErrorAction SilentlyContinue }
'ярлыки удалены'`, psq(dir))
	out, err := win.PS(time.Minute, script)
	if err != nil {
		return fmt.Errorf("не удалось очистить PATH и ярлыки: %w", err)
	}
	ui.Dim("%s", strings.ReplaceAll(out, "\n", "; "))

	// Запущенный exe удалить нельзя — удаляем папку после завершения процесса.
	exe, _ := os.Executable()
	if strings.HasPrefix(strings.ToLower(exe), strings.ToLower(dir)) {
		win.DetachedCmd(fmt.Sprintf(`ping -n 3 127.0.0.1 >nul & rmdir /s /q "%s"`, dir))
		ui.OK("Программа будет удалена через несколько секунд. Журналы и отчёты остались в %%LOCALAPPDATA%%\\WinFix.")
		return nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	ui.OK("Удалено. Журналы и отчёты остались в %%LOCALAPPDATA%%\\WinFix.")
	return nil
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o755)
}
