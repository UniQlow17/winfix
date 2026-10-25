//go:build windows

// sysrepair — диагностика и восстановление целостности Windows:
// SFC, DISM, CHKDSK, WMI, Центр обновления, Магазин, время, Defender.
package sysrepair

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"winfix/internal/platform/windows/win"
	"winfix/internal/ui"
)

func needAdmin() bool {
	if win.IsAdmin() {
		return true
	}
	ui.Fail("Эта операция требует прав администратора. Запустите WinFix от имени администратора.")
	return false
}

type eventStat struct {
	Label string `json:"Label"`
	Count int    `json:"Count"`
	Bad   bool   `json:"Bad"`
}

type diskInfo struct {
	Name   string `json:"Name"`
	Media  string `json:"Media"`
	Health string `json:"Health"`
	Oper   string `json:"Oper"`
	GB     int    `json:"GB"`
	Temp   *int   `json:"Temp"`
	Wear   *int   `json:"Wear"`
	Hours  *int   `json:"Hours"`
	RdErr  *int   `json:"RdErr"`
	WrErr  *int   `json:"WrErr"`
}

func opt(p *int, suffix string) string {
	if p == nil {
		return "—"
	}
	return strconv.Itoa(*p) + suffix
}

func diagnose() {
	ui.Header("Диагностика системы (только чтение)")

	// Ожидающая перезагрузка
	if out, _ := win.PS(30*time.Second, `
$p=$false
if(Test-Path 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending'){$p=$true}
if(Test-Path 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update\RebootRequired'){$p=$true}
if((Get-ItemProperty 'HKLM:\SYSTEM\CurrentControlSet\Control\Session Manager' -Name PendingFileRenameOperations -ErrorAction SilentlyContinue).PendingFileRenameOperations){$p=$true}
"$p"`); strings.Contains(out, "True") {
		ui.Warn("Ожидается перезагрузка (завершение обновлений/установок). Перезагрузите ПК перед ремонтом.")
	} else {
		ui.OK("Отложенных перезагрузок нет.")
	}

	// Диски
	var disks []diskInfo
	err := win.PSJSON(60*time.Second, `
$r=@(Get-PhysicalDisk -ErrorAction SilentlyContinue | ForEach-Object {
 $d=$_; $c=$null; try{$c=$d | Get-StorageReliabilityCounter -ErrorAction Stop}catch{}
 [pscustomobject]@{Name="$($d.FriendlyName)";Media="$($d.MediaType)";Health="$($d.HealthStatus)";Oper="$($d.OperationalStatus)";GB=[int]($d.Size/1GB);
  Temp=$(if($c){$c.Temperature}else{$null});Wear=$(if($c){$c.Wear}else{$null});Hours=$(if($c){$c.PowerOnHours}else{$null});
  RdErr=$(if($c){$c.ReadErrorsTotal}else{$null});WrErr=$(if($c){$c.WriteErrorsTotal}else{$null})} })
ConvertTo-Json -InputObject $r -Compress`, &disks)
	if err != nil {
		ui.Warn("Состояние дисков не получено: %v", err)
	}
	for _, d := range disks {
		line := fmt.Sprintf("%s (%s, %d ГБ): %s / %s; темп. %s, износ %s, наработка %s ч, ошибки чтения/записи %s/%s",
			d.Name, d.Media, d.GB, d.Health, d.Oper, opt(d.Temp, "°C"), opt(d.Wear, "%"), opt(d.Hours, ""), opt(d.RdErr, ""), opt(d.WrErr, ""))
		if d.Health != "Healthy" {
			ui.Fail("%s", line)
		} else if (d.Wear != nil && *d.Wear >= 80) || (d.Temp != nil && *d.Temp >= 70) {
			ui.Warn("%s", line)
		} else {
			ui.OK("%s", line)
		}
	}
	if !win.IsAdmin() {
		ui.Dim("(счётчики износа/температуры диска доступны только от администратора)")
	}

	// Тома: бит «грязного» тома
	if win.IsAdmin() {
		for _, v := range win.FixedDrives() {
			out, err := win.Run(20*time.Second, "fsutil.exe", "dirty", "query", v)
			if err != nil {
				continue
			}
			lo := strings.ToLower(out)
			switch {
			case strings.Contains(lo, "not dirty") || strings.Contains(lo, "не помечен") || strings.Contains(lo, "не загрязн"):
				ui.OK("Том %s: ошибок файловой системы не помечено.", v)
			case strings.Contains(lo, "dirty") || strings.Contains(lo, "загрязн"):
				ui.Warn("Том %s помечен как «грязный» — запустите «CHKDSK».", v)
			}
		}
	}

	// Службы ремонта
	var svcs []struct{ Name, Status, Start string }
	if win.PSJSON(30*time.Second, `
$r=@('wuauserv','bits','cryptsvc','TrustedInstaller','winmgmt','EventLog') | ForEach-Object { $s=Get-Service $_ -ErrorAction SilentlyContinue; if($s){[pscustomobject]@{Name=$s.Name;Status="$($s.Status)";Start="$($s.StartType)"}} }
ConvertTo-Json -InputObject @($r) -Compress`, &svcs) == nil {
		var bad []string
		for _, s := range svcs {
			if s.Start == "Disabled" {
				bad = append(bad, s.Name+" (отключена)")
			}
		}
		if len(bad) > 0 {
			ui.Warn("Отключены важные службы: %s", strings.Join(bad, ", "))
		} else {
			ui.OK("Ключевые службы (Windows Update, BITS, Crypto, WMI, Event Log) не отключены.")
		}
	}

	// WMI
	if out, err := win.Run(60*time.Second, "winmgmt", "/verifyrepository"); err == nil || strings.Contains(strings.ToLower(out), "consistent") {
		if strings.Contains(strings.ToLower(out), "inconsistent") || strings.Contains(out, "несогласован") {
			ui.Warn("Репозиторий WMI несогласован — команда «Починить репозиторий WMI».")
		} else {
			ui.OK("Репозиторий WMI согласован.")
		}
	}

	// Последнее обновление
	if out, _ := win.PS(30*time.Second, `try{ (New-Object -ComObject Microsoft.Update.AutoUpdate).Results.LastInstallationSuccessDate.ToString('yyyy-MM-dd') }catch{}`); out != "" {
		if t, err := time.Parse("2006-01-02", out); err == nil && time.Since(t) > 60*24*time.Hour {
			ui.Warn("Последняя успешная установка обновлений: %s (больше 60 дней назад).", out)
		} else {
			ui.OK("Последняя успешная установка обновлений: %s", out)
		}
	}

	// События за 7 дней
	var ev []eventStat
	err = win.PSJSON(3*time.Minute, `
$since=(Get-Date).AddDays(-7)
function C($f){ try{ @(Get-WinEvent -FilterHashtable $f -ErrorAction Stop).Count }catch{0} }
$r=@(
 [pscustomobject]@{Label='Жёсткие перезагрузки/зависания (Kernel-Power 41)';Bad=$true;Count=(C @{LogName='System';ProviderName='Microsoft-Windows-Kernel-Power';Id=41;StartTime=$since})},
 [pscustomobject]@{Label='Синие экраны (BugCheck)';Bad=$true;Count=(C @{LogName='System';ProviderName='Microsoft-Windows-WER-SystemErrorReporting';Id=1001;StartTime=$since})},
 [pscustomobject]@{Label='Ошибки диска (disk 7/11/51/153)';Bad=$true;Count=(C @{LogName='System';ProviderName='disk';Id=7,11,51,153;StartTime=$since})},
 [pscustomobject]@{Label='Ошибки файловой системы (NTFS)';Bad=$true;Count=(C @{LogName='System';ProviderName='Ntfs';Id=55,98,137,140;StartTime=$since})},
 [pscustomobject]@{Label='Аппаратные ошибки (WHEA-Logger)';Bad=$true;Count=(C @{LogName='System';ProviderName='Microsoft-Windows-WHEA-Logger';StartTime=$since})},
 [pscustomobject]@{Label='Сбои видеодрайвера (Display 4101, TDR)';Bad=$true;Count=(C @{LogName='System';ProviderName='Display';Id=4101;StartTime=$since})},
 [pscustomobject]@{Label='Падения служб (SCM 7031/7034)';Bad=$false;Count=(C @{LogName='System';ProviderName='Service Control Manager';Id=7031,7034;StartTime=$since})},
 [pscustomobject]@{Label='Зависания приложений (Application Hang)';Bad=$false;Count=(C @{LogName='Application';ProviderName='Application Hang';StartTime=$since})},
 [pscustomobject]@{Label='Падения приложений (Application Error)';Bad=$false;Count=(C @{LogName='Application';ProviderName='Application Error';StartTime=$since})}
)
ConvertTo-Json -InputObject $r -Compress`, &ev)
	ui.Header("События за 7 дней")
	if err != nil {
		ui.Warn("Журналы событий не прочитаны: %v", err)
	}
	for _, e := range ev {
		switch {
		case e.Count == 0:
			ui.OK("%s: 0", e.Label)
		case e.Bad:
			ui.Warn("%s: %d", e.Label, e.Count)
		default:
			ui.Info("%s: %d", e.Label, e.Count)
		}
	}
	ui.Info("")
	ui.Info("Что делать при проблемах: диск/NTFS → CHKDSK; WHEA/BugCheck → winfix memory и winfix health;")
	ui.Info("TDR → переустановка видеодрайвера (winfix drivers); повреждения Windows → SFC и DISM.")

	if win.IsAdmin() {
		ui.Header("Хранилище компонентов (DISM CheckHealth)")
		if code, _ := win.Stream("Dism.exe", "/Online", "/Cleanup-Image", "/CheckHealth"); code != 0 {
			ui.Warn("DISM сообщил о проблеме (код %d) — выполните DISM.", code)
		}
	} else {
		ui.Dim("(проверка хранилища компонентов DISM требует прав администратора)")
	}
}

func sfc() {
	ui.Header("SFC — проверка и восстановление системных файлов")
	if !needAdmin() {
		return
	}
	ui.Step("Запускаю sfc /scannow (5–15 минут, окно не закрывайте)…")
	code, text, err := win.StreamText("sfc.exe", "/scannow")
	lo := strings.ToLower(text)
	switch {
	case strings.Contains(lo, "не обнаружила нарушений") || strings.Contains(lo, "did not find any integrity violations"):
		ui.OK("Нарушений целостности не найдено.")
	case strings.Contains(lo, "успешно их восстановила") || strings.Contains(lo, "successfully repaired"):
		ui.OK("Повреждённые файлы найдены и восстановлены. Рекомендуется перезагрузка.")
	case strings.Contains(lo, "не может восстановить") || strings.Contains(lo, "unable to fix"):
		ui.Fail("Часть файлов восстановить не удалось: выполните DISM (RestoreHealth), затем повторите SFC.")
		ui.Info("Подробности: findstr /c:\"[SR]\" %%windir%%\\Logs\\CBS\\CBS.log")
	case err != nil:
		ui.Fail("SFC завершился с ошибкой (код %d): %v", code, err)
	default:
		ui.Info("SFC завершён (код %d). Если есть сомнения — смотрите вывод выше.", code)
	}
}

func dism() {
	ui.Header("DISM — восстановление хранилища компонентов")
	if !needAdmin() {
		return
	}
	for _, step := range [][]string{
		{"/Online", "/Cleanup-Image", "/ScanHealth"},
		{"/Online", "/Cleanup-Image", "/RestoreHealth"},
	} {
		ui.Step("Dism.exe %s (может занять 10–30 минут, нужен интернет)", strings.Join(step, " "))
		code, err := win.Stream("Dism.exe", step...)
		if err != nil || code != 0 {
			ui.Fail("Шаг завершился с кодом %d.", code)
			ui.Info("Если ошибка 0x800f081f/0x800f0906 — нет доступа к источнику файлов; проверьте интернет и повторите.")
			return
		}
	}
	ui.OK("Хранилище компонентов проверено и восстановлено. Теперь имеет смысл повторить SFC.")
}

func chkdskScan() {
	ui.Header("CHKDSK — онлайн-проверка системного тома")
	if !needAdmin() {
		return
	}
	code, err := win.Stream("chkdsk.exe", sysDrive(), "/scan")
	if err != nil || code != 0 {
		ui.Warn("CHKDSK сообщил о проблемах (код %d). Запустите полную проверку при загрузке (chkdsk-full).", code)
		return
	}
	ui.OK("Ошибок файловой системы не обнаружено.")
}

func chkdskFull() {
	ui.Header("CHKDSK — полная проверка при следующей загрузке")
	if !needAdmin() {
		return
	}
	ui.Info("Будет назначена проверка системного тома с исправлением ошибок и поиском плохих секторов (/F /R).")
	ui.Info("Она выполняется при перезагрузке и на большом SSD может занять от 10 минут до часа.")
	if !ui.Confirm("Назначить проверку на следующую загрузку?", false) {
		return
	}
	win.StreamStdin("Y\r\n", "cmd.exe", "/c", "chkdsk "+sysDrive()+" /F /R")
	if ui.Confirm("Перезагрузить компьютер сейчас?", false) {
		win.Run(10*time.Second, "shutdown.exe", "/r", "/t", "15", "/c", "WinFix: проверка диска")
	}
}

func resetWU() {
	ui.Header("Сброс компонентов Центра обновления Windows")
	if !needAdmin() {
		return
	}
	ui.Info("Служба обновлений будет остановлена, папки SoftwareDistribution и catroot2 переименованы (создадутся заново).")
	ui.Info("История обновлений сохранится. Помогает при ошибках установки/поиска обновлений.")
	if !ui.Confirm("Продолжить?", false) {
		return
	}
	code, err := win.StreamPS(`
$ts=Get-Date -Format yyyyMMdd-HHmmss
$svc='wuauserv','bits','cryptsvc','msiserver','appidsvc'
foreach($s in $svc){ Stop-Service $s -Force -ErrorAction SilentlyContinue; "stop $s" }
Start-Sleep 2
foreach($p in "$env:SystemRoot\SoftwareDistribution","$env:SystemRoot\System32\catroot2"){
  if(Test-Path $p){ try{ Rename-Item $p "$(Split-Path $p -Leaf).bak-$ts" -ErrorAction Stop; "rename $p" }catch{ "НЕ УДАЛОСЬ переименовать $p : $($_.Exception.Message)" } } }
Get-ChildItem "$env:ALLUSERSPROFILE\Microsoft\Network\Downloader" -Filter 'qmgr*.dat' -ErrorAction SilentlyContinue | Remove-Item -Force -ErrorAction SilentlyContinue
foreach($s in $svc){ Start-Service $s -ErrorAction SilentlyContinue; "start $s" }
"Готово"`)
	if err != nil || code != 0 {
		ui.Fail("Сброс завершился с ошибкой (код %d)", code)
		return
	}
	ui.OK("Готово. Перезагрузите ПК и проверьте обновления. Старые папки *.bak-… можно удалить позже.")
}

func fixWMI() {
	ui.Header("Починка репозитория WMI")
	if !needAdmin() {
		return
	}
	out, _ := win.Run(60*time.Second, "winmgmt", "/verifyrepository")
	ui.Info("Проверка: %s", strings.TrimSpace(out))
	lo := strings.ToLower(out)
	if !strings.Contains(lo, "inconsistent") && !strings.Contains(out, "несогласован") && !ui.Confirm("Репозиторий выглядит согласованным. Всё равно восстановить?", false) {
		return
	}
	out, err := win.Run(5*time.Minute, "winmgmt", "/salvagerepository")
	ui.Info("%s", strings.TrimSpace(out))
	if err != nil {
		ui.Fail("Не удалось: %v", err)
		return
	}
	ui.OK("Репозиторий WMI восстановлен.")
}

func resetStore() {
	ui.Header("Сброс кэша Microsoft Store и перерегистрация встроенных приложений")
	ui.Info("1) wsreset — очистка кэша Магазина (откроется окно Магазина).")
	if ui.Confirm("Выполнить wsreset?", true) {
		win.Run(3*time.Minute, "wsreset.exe")
		ui.OK("wsreset выполнен.")
	}
	ui.Info("2) Перерегистрация встроенных приложений — лечит неработающие «Параметры», «Пуск», Магазин, Калькулятор и т.п. (1–5 минут, возможны безобидные предупреждения).")
	if ui.Confirm("Выполнить перерегистрацию?", false) {
		if !needAdmin() {
			return
		}
		win.StreamPS(`Get-AppxPackage -AllUsers | Where-Object {$_.InstallLocation -and $_.NonRemovable -eq $false} | ForEach-Object {
 try{ Add-AppxPackage -DisableDevelopmentMode -Register "$($_.InstallLocation)\AppXManifest.xml" -ErrorAction Stop }catch{} }; 'Готово'`)
	}
}

func syncTime() {
	ui.Header("Синхронизация времени")
	if !needAdmin() {
		return
	}
	win.Run(30*time.Second, "sc.exe", "config", "w32time", "start=", "auto")
	win.Run(30*time.Second, "net.exe", "start", "w32time")
	out, err := win.Run(60*time.Second, "w32tm.exe", "/resync", "/force")
	ui.Info("%s", strings.TrimSpace(out))
	if err != nil {
		ui.Warn("Синхронизация не удалась — проверьте интернет.")
		return
	}
	ui.OK("Время синхронизировано.")
}

func defender() {
	ui.Header("Microsoft Defender")
	if !needAdmin() {
		return
	}
	ui.Step("Обновляю антивирусные базы…")
	win.StreamPS(`Update-MpSignature -ErrorAction SilentlyContinue; $s=Get-MpComputerStatus; "Базы: $($s.AntivirusSignatureLastUpdated); защита в реальном времени: $($s.RealTimeProtectionEnabled)"`)
	if ui.Confirm("Запустить быстрое сканирование?", true) {
		win.StreamPS(`Start-MpScan -ScanType QuickScan; $t=Get-MpThreat -ErrorAction SilentlyContinue; if($t){ $t | Format-Table ThreatID,IsActive -AutoSize | Out-String } else { 'Угроз не найдено' }`)
	}
}

func full() {
	diagnose()
	sfc()
	dism()
	chkdskScan()
	ui.Header("Полный цикл завершён")
	ui.Info("Рекомендуется перезагрузка.")
}

func sysDrive() string {
	if d := os.Getenv("SystemDrive"); d != "" {
		return d
	}
	return "C:"
}
