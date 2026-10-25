//go:build windows

// healthcheck — общий отчёт о состоянии компьютера с рекомендациями,
// какой утилитой WinFix что исправлять.
package healthcheck

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"winfix/internal/core"
	"winfix/internal/platform/windows/win"
	"winfix/internal/ui"
)

type finding struct {
	level string // ok | warn | fail
	text  string
	tool  string
}

var (
	findings []finding
	plain    []string
)

func rec(level, text, tool string) {
	findings = append(findings, finding{level, text, tool})
	mark := map[string]string{"ok": "[ OK ]", "warn": "[ !! ]", "fail": "[FAIL]"}[level]
	plain = append(plain, mark+" "+text)
	switch level {
	case "ok":
		ui.OK("%s", text)
	case "warn":
		ui.Warn("%s", text)
	default:
		ui.Fail("%s", text)
	}
}

func note(f string, a ...any) {
	plain = append(plain, "       "+fmt.Sprintf(f, a...))
	ui.Info(f, a...)
}

func section(t string) {
	plain = append(plain, "", "== "+t+" ==")
	ui.Header(t)
}

type sysInfo struct {
	OS      string
	Boot    string
	Model   string
	CPU     string
	Cores   int
	Threads int
	BIOS    string
	GPU     []string
	Power   string
}

type volume struct {
	Letter string
	Label  string
	FS     string
	SizeGB float64
	FreeGB float64
}

type disk struct {
	Name, Media, Health string
	GB                  int
}

type evStat struct {
	Label  string
	Count  int
	Recent int
	Bad    bool
}

type flood struct {
	Provider string
	Count    int
}

type health struct {
	Defender struct {
		Realtime bool
		SigAge   int
		Present  bool
	}
	Firewall []string
	Updates  string
	Pending  bool
	Problems []string
	Startup  []string
	Battery  *struct{ Design, Full int }
}

func run(args []string) {
	fs := flag.NewFlagSet("health", flag.ContinueOnError)
	noSave := fs.Bool("no-save", false, "не сохранять отчёт в файл")
	if fs.Parse(args) != nil {
		return
	}
	findings, plain = nil, nil
	ui.Header("Проверка здоровья компьютера")
	ui.Step("Собираю данные (около 30–60 секунд)…")

	var (
		wg     sync.WaitGroup
		si     sysInfo
		vols   []volume
		disks  []disk
		evs    []evStat
		floods []flood
		h      health
		errSys error
	)
	run := func(f func()) {
		wg.Add(1)
		go func() { defer wg.Done(); f() }()
	}
	run(func() {
		errSys = win.PSJSON(60*time.Second, `
$os=Get-CimInstance Win32_OperatingSystem; $cs=Get-CimInstance Win32_ComputerSystem; $cpu=Get-CimInstance Win32_Processor | Select-Object -First 1; $bios=Get-CimInstance Win32_BIOS
$gpu=@(Get-CimInstance Win32_VideoController | ForEach-Object { "$($_.Name) (драйвер $($_.DriverVersion))" })
$pw=(powercfg /getactivescheme) -replace '.*\((.*)\).*','$1'
ConvertTo-Json -Depth 3 -Compress -InputObject ([pscustomobject]@{OS="$($os.Caption) $($os.Version)";Boot=$os.LastBootUpTime.ToString('o');Model="$($cs.Manufacturer) $($cs.Model)";CPU="$($cpu.Name)";
 Cores=[int]$cpu.NumberOfCores;Threads=[int]$cpu.NumberOfLogicalProcessors;BIOS="$($bios.SMBIOSBIOSVersion) ($($bios.ReleaseDate.ToString('yyyy-MM-dd')))";GPU=$gpu;Power="$pw"})`, &si)
	})
	run(func() {
		win.PSJSON(60*time.Second, `
$r=@(Get-Volume -ErrorAction SilentlyContinue | Where-Object {$_.DriveLetter -and $_.Size -gt 0} | ForEach-Object {[pscustomobject]@{Letter="$($_.DriveLetter):";Label="$($_.FileSystemLabel)";FS="$($_.FileSystem)";SizeGB=[math]::Round($_.Size/1GB,1);FreeGB=[math]::Round($_.SizeRemaining/1GB,1)}})
ConvertTo-Json -InputObject $r -Compress`, &vols)
	})
	run(func() {
		win.PSJSON(60*time.Second, `
$r=@(Get-PhysicalDisk -ErrorAction SilentlyContinue | ForEach-Object {[pscustomobject]@{Name="$($_.FriendlyName)";Media="$($_.MediaType)";Health="$($_.HealthStatus)";GB=[int]($_.Size/1GB)}})
ConvertTo-Json -InputObject $r -Compress`, &disks)
	})
	run(func() {
		win.PSJSON(4*time.Minute, `
$s=(Get-Date).AddDays(-7); $h=(Get-Date).AddMinutes(-10)
function C($f,$t){ try{ @(Get-WinEvent -FilterHashtable (@{LogName='System';StartTime=$t}+$f) -ErrorAction Stop).Count }catch{0} }
function E($label,$bad,$f){ [pscustomobject]@{Label=$label;Bad=$bad;Count=(C $f $s);Recent=(C $f $h)} }
$r=@(
 (E 'Жёсткие перезагрузки/зависания (Kernel-Power 41)' $true @{ProviderName='Microsoft-Windows-Kernel-Power';Id=41}),
 (E 'Синие экраны (BugCheck)' $true @{ProviderName='Microsoft-Windows-WER-SystemErrorReporting';Id=1001}),
 (E 'Аппаратные ошибки шины/памяти (WHEA)' $true @{ProviderName='Microsoft-Windows-WHEA-Logger'}),
 (E 'Ошибки диска' $true @{ProviderName='disk';Id=7,11,51,153}),
 (E 'Ошибки файловой системы NTFS' $true @{ProviderName='Ntfs';Id=55,98,137,140}),
 (E 'Сбои видеодрайвера (TDR)' $true @{ProviderName='Display';Id=4101})
)
ConvertTo-Json -InputObject $r -Compress`, &evs)
	})
	run(func() {
		win.PSJSON(3*time.Minute, `
$r=@(Get-WinEvent -FilterHashtable @{LogName='System';Level=1,2;StartTime=(Get-Date).AddMinutes(-10)} -ErrorAction SilentlyContinue | Group-Object ProviderName | Where-Object {$_.Count -ge 20} |
 Sort-Object Count -Descending | Select-Object -First 5 | ForEach-Object {[pscustomobject]@{Provider=$_.Name;Count=$_.Count}})
ConvertTo-Json -InputObject $r -Compress`, &floods)
	})
	run(func() {
		win.PSJSON(2*time.Minute, `
$d=Get-MpComputerStatus -ErrorAction SilentlyContinue
$fw=@(Get-NetFirewallProfile -ErrorAction SilentlyContinue | ForEach-Object { "$($_.Name)=$($_.Enabled)" })
$upd=''; try{ $upd=(New-Object -ComObject Microsoft.Update.AutoUpdate).Results.LastInstallationSuccessDate.ToString('yyyy-MM-dd') }catch{}
$p=$false
if(Test-Path 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending'){$p=$true}
if(Test-Path 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update\RebootRequired'){$p=$true}
$prob=@(Get-PnpDevice -PresentOnly -ErrorAction SilentlyContinue | Where-Object {$_.Status -ne 'OK' -and $_.Status -ne 'Unknown'} | ForEach-Object {"$($_.FriendlyName) [$($_.Problem)]"})
$st=@()
foreach($pair in @(@('HKCU:\Software\Microsoft\Windows\CurrentVersion\Run','HKCU:\Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run'),@('HKLM:\Software\Microsoft\Windows\CurrentVersion\Run','HKLM:\Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run'))){
 $run=Get-ItemProperty $pair[0] -ErrorAction SilentlyContinue; $ap=Get-ItemProperty $pair[1] -ErrorAction SilentlyContinue
 if($run){ foreach($x in $run.PSObject.Properties){ if($x.Name -like 'PS*'){continue}; $en=$true
  if($ap -and $ap.PSObject.Properties[$x.Name]){ $b=$ap.PSObject.Properties[$x.Name].Value; if($b -is [byte[]] -and ($b[0] % 2) -eq 1){$en=$false} }
  if($en){$st+=$x.Name} } } }
$bat=$null; try{ $dc=(Get-CimInstance -Namespace root\wmi -ClassName BatteryStaticData -ErrorAction Stop).DesignedCapacity; $fc=(Get-CimInstance -Namespace root\wmi -ClassName BatteryFullChargedCapacity -ErrorAction Stop).FullChargedCapacity; if($dc -gt 0){$bat=[pscustomobject]@{Design=[int]$dc;Full=[int]$fc}} }catch{}
ConvertTo-Json -Depth 4 -Compress -InputObject ([pscustomobject]@{Defender=[pscustomobject]@{Present=[bool]$d;Realtime=$(if($d){[bool]$d.RealTimeProtectionEnabled}else{$false});SigAge=$(if($d){[int]$d.AntivirusSignatureAge}else{0})};
 Firewall=$fw;Updates=$upd;Pending=$p;Problems=$prob;Startup=$st;Battery=$bat})`, &h)
	})
	wg.Wait()

	plain = append(plain, "WinFix — отчёт о состоянии компьютера", time.Now().Format("2006-01-02 15:04:05"))

	// --- Система
	section("Система")
	if errSys == nil {
		note("%s, %s", si.Model, si.OS)
		note("Процессор: %s (%d ядер / %d потоков)", si.CPU, si.Cores, si.Threads)
		for _, g := range si.GPU {
			note("Видео: %s", g)
		}
		note("BIOS: %s; схема питания: %s", si.BIOS, si.Power)
		if b, err := time.Parse(time.RFC3339Nano, si.Boot); err == nil {
			up := time.Since(b)
			days := int(up.Hours() / 24)
			if days >= 14 {
				rec("warn", fmt.Sprintf("Аптайм %d дн. — перезагрузка не делалась давно (Быстрый запуск Windows копит сбои).", days), "")
			} else {
				rec("ok", fmt.Sprintf("Аптайм: %dд %dч", days, int(up.Hours())%24), "")
			}
		}
	} else {
		rec("warn", fmt.Sprintf("Сведения о системе не получены: %v", errSys), "")
	}
	m := win.Memory()
	pi := win.Performance()
	switch {
	case m.Load >= 90:
		rec("fail", fmt.Sprintf("Память: занято %d%% из %s", m.Load, ui.FormatBytes(int64(m.TotalPhys))), "memory")
	case m.Load >= 80:
		rec("warn", fmt.Sprintf("Память: занято %d%% из %s", m.Load, ui.FormatBytes(int64(m.TotalPhys))), "memory")
	default:
		rec("ok", fmt.Sprintf("Память: занято %d%% из %s", m.Load, ui.FormatBytes(int64(m.TotalPhys))), "")
	}
	if pi.CommitLimit > 0 && float64(pi.CommitTotal)/float64(pi.CommitLimit) > 0.85 {
		rec("warn", "Commit памяти выше 85% лимита — риск ошибок «недостаточно памяти»", "memory")
	}

	// --- Диски
	section("Диски")
	for _, d := range disks {
		if d.Health != "Healthy" {
			rec("fail", fmt.Sprintf("Диск %s (%s, %d ГБ): состояние %s", d.Name, d.Media, d.GB, d.Health), "repair")
		} else {
			rec("ok", fmt.Sprintf("Диск %s (%s, %d ГБ): Healthy", d.Name, d.Media, d.GB), "")
		}
	}
	for _, v := range vols {
		pct := 0.0
		if v.SizeGB > 0 {
			pct = v.FreeGB / v.SizeGB * 100
		}
		txt := fmt.Sprintf("%s [%s] %s: свободно %.0f ГБ из %.0f ГБ (%.0f%%)", v.Letter, v.Label, v.FS, v.FreeGB, v.SizeGB, pct)
		switch {
		case pct < 10 || v.FreeGB < 10:
			rec("fail", txt+" — мало места", "junk")
		case pct < 20:
			rec("warn", txt, "junk")
		default:
			rec("ok", txt, "")
		}
	}

	// --- События
	section("События за 7 дней")
	for _, e := range evs {
		txt := fmt.Sprintf("%s: %d", e.Label, e.Count)
		tool := "repair"
		if strings.Contains(e.Label, "WHEA") {
			tool = "memory / network"
		}
		if strings.Contains(e.Label, "TDR") {
			tool = "drivers"
		}
		switch {
		case e.Count == 0:
			rec("ok", txt, "")
		case e.Recent > 0:
			rec("fail", txt+fmt.Sprintf(" (за последние 10 мин: %d — ПРОДОЛЖАЕТСЯ)", e.Recent), tool)
		default:
			rec("warn", txt+" (за последние 10 мин — нет)", tool)
		}
	}
	for _, f := range floods {
		rec("fail", fmt.Sprintf("Поток ошибок за последние 10 мин: %s — %d событий", f.Provider, f.Count), "network / drivers")
	}

	// --- Безопасность и обновления
	section("Безопасность и обновления")
	if h.Defender.Present {
		switch {
		case !h.Defender.Realtime:
			rec("warn", "Защита Defender в реальном времени выключена (если нет другого антивируса — включите).", "repair")
		case h.Defender.SigAge > 7:
			rec("warn", fmt.Sprintf("Базы Defender устарели на %d дн.", h.Defender.SigAge), "repair")
		default:
			rec("ok", fmt.Sprintf("Defender активен, базам %d дн.", h.Defender.SigAge), "")
		}
	} else {
		note("Defender не отвечает (возможно, установлен сторонний антивирус).")
	}
	var fwOff []string
	for _, f := range h.Firewall {
		if strings.HasSuffix(f, "=False") {
			fwOff = append(fwOff, strings.TrimSuffix(f, "=False"))
		}
	}
	if len(fwOff) > 0 {
		rec("warn", "Брандмауэр выключен для профилей: "+strings.Join(fwOff, ", "), "")
	} else if len(h.Firewall) > 0 {
		rec("ok", "Брандмауэр Windows включён.", "")
	}
	if h.Updates != "" {
		if t, err := time.Parse("2006-01-02", h.Updates); err == nil && time.Since(t) > 60*24*time.Hour {
			rec("warn", "Последняя установка обновлений: "+h.Updates+" (>60 дней)", "repair")
		} else {
			rec("ok", "Последняя установка обновлений: "+h.Updates, "")
		}
	}
	if h.Pending {
		rec("warn", "Ожидается перезагрузка для завершения обновлений/установок.", "")
	}

	// --- Устройства
	section("Устройства и драйверы")
	real := 0
	for _, p := range h.Problems {
		switch {
		case strings.Contains(p, "Dynamic Tuning Technology"):
			note("Intel DTT: известный безвредный «призрак» (питанием CPU управляет Intel IPF). Удаляется командой «winfix drivers problems». [%s]", p)
		case strings.Contains(p, "CM_PROB_DISABLED"):
			note("Отключено вручную: %s", p)
		default:
			rec("warn", "Проблемное устройство: "+p, "drivers")
			real++
		}
	}
	if real == 0 {
		rec("ok", "Проблемных устройств нет (не считая известных безвредных).", "")
	}

	// --- Прочее
	section("Автозагрузка и питание")
	if len(h.Startup) > 8 {
		rec("warn", fmt.Sprintf("В автозагрузке %d приложений: %s", len(h.Startup), strings.Join(h.Startup, ", ")), "")
	} else {
		rec("ok", fmt.Sprintf("Автозагрузка (включено %d): %s", len(h.Startup), strings.Join(h.Startup, ", ")), "")
	}
	if h.Battery != nil && h.Battery.Design > 0 {
		pct := h.Battery.Full * 100 / h.Battery.Design
		txt := fmt.Sprintf("Батарея: ёмкость %d%% от заводской (%d из %d мВт·ч)", pct, h.Battery.Full, h.Battery.Design)
		if pct < 60 {
			rec("warn", txt+" — сильный износ", "")
		} else {
			rec("ok", txt, "")
		}
	}

	// --- Итог
	section("Итог")
	var nf, nw int
	tools := map[string][]string{}
	for _, f := range findings {
		switch f.level {
		case "fail":
			nf++
		case "warn":
			nw++
		}
		if f.level != "ok" && f.tool != "" {
			tools[f.tool] = append(tools[f.tool], f.text)
		}
	}
	if nf == 0 && nw == 0 {
		rec("ok", "Серьёзных проблем не обнаружено.", "")
	} else {
		ui.Info("Критичных: %d, предупреждений: %d", nf, nw)
		plain = append(plain, fmt.Sprintf("Критичных: %d, предупреждений: %d", nf, nw))
		for t, l := range tools {
			ui.Info("→ запустите %swinfix %s%s (%d замеч.)", ui.Cyan, t, ui.Reset, len(l))
			plain = append(plain, "→ утилита winfix "+t)
		}
	}

	if !*noSave {
		p := filepath.Join(core.DataDir("reports"), "healthcheck-"+time.Now().Format("20060102-150405")+".txt")
		if os.WriteFile(p, []byte("\xef\xbb\xbf"+strings.Join(plain, "\r\n")+"\r\n"), 0o644) == nil {
			ui.Dim("Отчёт сохранён: %s", p)
		}
	}
}
