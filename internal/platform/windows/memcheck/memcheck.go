//go:build windows

// memcheck — диагностика и настройка памяти: commit/pagefile, «пожиратели»,
// утечки дескрипторов, модули RAM, тест памяти, лимиты WSL.
package memcheck

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"winfix/internal/platform/windows/win"
	"winfix/internal/ui"
)

type proc struct {
	Name    string `json:"Name"`
	ID      int    `json:"Id"`
	WS      int64  `json:"WS"`
	Private int64  `json:"Private"`
	Handles int    `json:"Handles"`
}

type ramModule struct {
	Bank  string `json:"Bank"`
	GB    int    `json:"GB"`
	Speed int    `json:"Speed"`
	Conf  int    `json:"Conf"`
	Maker string `json:"Maker"`
	Part  string `json:"Part"`
	Type  int    `json:"Type"`
}

func bar(pct int) string { return ui.Bar(float64(pct)/100, 20) }

func overview() {
	ui.Header("Состояние памяти")
	m := win.Memory()
	p := win.Performance()
	used := m.TotalPhys - m.AvailPhys
	line := fmt.Sprintf("Физическая: занято %s из %s (%d%%) %s", ui.FormatBytes(int64(used)), ui.FormatBytes(int64(m.TotalPhys)), m.Load, bar(int(m.Load)))
	switch {
	case m.Load >= 90:
		ui.Fail("%s", line)
	case m.Load >= 80:
		ui.Warn("%s", line)
	default:
		ui.OK("%s", line)
	}
	if p.CommitLimit > 0 {
		pct := int(float64(p.CommitTotal) / float64(p.CommitLimit) * 100)
		line = fmt.Sprintf("Commit (выделено): %s из лимита %s (%d%%), пик %s", ui.FormatBytes(int64(p.CommitTotal)), ui.FormatBytes(int64(p.CommitLimit)), pct, ui.FormatBytes(int64(p.CommitPeak)))
		if pct >= 85 {
			ui.Warn("%s — близко к лимиту: возможны ошибки «недостаточно памяти». Включите автоматический файл подкачки (winfix memory pagefile).", line)
		} else {
			ui.OK("%s", line)
		}
		ui.Info("Кэш системы: %s; пул ядра: paged %s, nonpaged %s", ui.FormatBytes(int64(p.SystemCache)), ui.FormatBytes(int64(p.KernelPaged)), ui.FormatBytes(int64(p.KernelNonpaged)))
		if p.KernelNonpaged > 2<<30 {
			ui.Warn("Nonpaged pool больше 2 ГБ — признак утечки в драйвере (часто антивирус, сеть, виртуализация). Используйте poolmon/WPR.")
		}
		ui.Info("Процессов: %d, потоков: %d, дескрипторов: %d", p.Processes, p.Threads, p.Handles)
	}

	var cfg struct {
		Auto  bool `json:"Auto"`
		Files []struct {
			Name    string `json:"Name"`
			Alloc   int    `json:"Alloc"`
			Current int    `json:"Current"`
			Peak    int    `json:"Peak"`
		} `json:"Files"`
		MM string `json:"MM"`
	}
	if err := win.PSJSON(30*time.Second, `
$cs=Get-CimInstance Win32_ComputerSystem
$pf=@(Get-CimInstance Win32_PageFileUsage | ForEach-Object {[pscustomobject]@{Name="$($_.Name)";Alloc=[int]$_.AllocatedBaseSize;Current=[int]$_.CurrentUsage;Peak=[int]$_.PeakUsage}})
$mm=''; try{ $a=Get-MMAgent -ErrorAction Stop; if($null -ne $a.MemoryCompression){ $mm="сжатие памяти: $($a.MemoryCompression); комбинирование страниц: $($a.PageCombining); префетч приложений: $($a.ApplicationLaunchPrefetching)" } }catch{}
ConvertTo-Json -InputObject ([pscustomobject]@{Auto=[bool]$cs.AutomaticManagedPagefile;Files=$pf;MM=$mm}) -Depth 4 -Compress`, &cfg); err == nil {
		for _, f := range cfg.Files {
			ui.Info("Файл подкачки %s: выделено %d МБ, сейчас %d МБ, пик %d МБ", f.Name, f.Alloc, f.Current, f.Peak)
		}
		if cfg.Auto {
			ui.OK("Размер файла подкачки управляется Windows (рекомендуется).")
		} else {
			ui.Warn("Файл подкачки задан вручную. Для большинства систем лучше автоматический режим (winfix memory pagefile).")
		}
		if cfg.MM != "" {
			ui.Info("%s", cfg.MM)
		}
	}
}

func processes() []proc {
	var ps []proc
	err := win.PSJSON(60*time.Second, `
$r=@(Get-Process -ErrorAction SilentlyContinue | ForEach-Object {[pscustomobject]@{Name=$_.ProcessName;Id=$_.Id;WS=[int64]$_.WorkingSet64;Private=[int64]$_.PrivateMemorySize64;Handles=[int]$_.HandleCount}})
ConvertTo-Json -InputObject $r -Compress`, &ps)
	if err != nil {
		ui.Fail("Не удалось получить процессы: %v", err)
	}
	return ps
}

func top() {
	ui.Header("Топ потребителей памяти (с группировкой по имени)")
	ps := processes()
	type agg struct {
		ws, priv int64
		n, h     int
	}
	g := map[string]*agg{}
	for _, p := range ps {
		a := g[p.Name]
		if a == nil {
			a = &agg{}
			g[p.Name] = a
		}
		a.ws += p.WS
		a.priv += p.Private
		a.n++
		if p.Handles > a.h {
			a.h = p.Handles
		}
	}
	type kv struct {
		k string
		v *agg
	}
	var l []kv
	for k, v := range g {
		l = append(l, kv{k, v})
	}
	sort.Slice(l, func(i, j int) bool { return l[i].v.ws > l[j].v.ws })
	rows := [][]string{}
	for i, e := range l {
		if i >= 15 {
			break
		}
		rows = append(rows, []string{e.k, strconv.Itoa(e.v.n), ui.FormatBytes(e.v.ws), ui.FormatBytes(e.v.priv)})
	}
	ui.Table([]string{"Процесс", "Экз.", "В памяти", "Private"}, rows)

	ui.Header("Подозрение на утечки")
	found := false
	for _, p := range ps {
		switch {
		case p.Handles > 20000:
			ui.Warn("%s (PID %d): %d дескрипторов — возможна утечка handle'ов", p.Name, p.ID, p.Handles)
			found = true
		case p.Private > 4<<30:
			ui.Warn("%s (PID %d): private %s — очень много; перезапустите, если приложение давно работает", p.Name, p.ID, ui.FormatBytes(p.Private))
			found = true
		}
	}
	if !found {
		ui.OK("Явных признаков утечек нет (дескрипторов >20 000 или private >4 ГБ ни у кого).")
	}
	for _, p := range ps {
		if strings.HasPrefix(strings.ToLower(p.Name), "vmmem") {
			ui.Info("%s занимает %s — это WSL2/Docker. Ограничить можно командой «winfix memory wsl».", p.Name, ui.FormatBytes(p.WS))
		}
	}
}

func modules() {
	ui.Header("Модули оперативной памяти")
	var ms []ramModule
	err := win.PSJSON(30*time.Second, `
$r=@(Get-CimInstance Win32_PhysicalMemory | ForEach-Object {[pscustomobject]@{Bank="$($_.BankLabel) / $($_.DeviceLocator)";GB=[int]($_.Capacity/1GB);Speed=[int]$_.Speed;Conf=[int]$_.ConfiguredClockSpeed;Maker="$($_.Manufacturer)".Trim();Part="$($_.PartNumber)".Trim();Type=[int]$_.SMBIOSMemoryType}})
ConvertTo-Json -InputObject $r -Compress`, &ms)
	if err != nil {
		ui.Fail("Не удалось прочитать модули: %v", err)
		return
	}
	rows := [][]string{}
	slow := false
	for _, m := range ms {
		typ := map[int]string{26: "DDR4", 34: "DDR5", 24: "DDR3"}[m.Type]
		rows = append(rows, []string{m.Bank, fmt.Sprintf("%d ГБ", m.GB), typ, fmt.Sprintf("%d / %d МГц", m.Conf, m.Speed), m.Maker, m.Part})
		if m.Conf > 0 && m.Speed > 0 && m.Conf < m.Speed {
			slow = true
		}
	}
	ui.Table([]string{"Слот", "Объём", "Тип", "Работает / номинал", "Производитель", "Part №"}, rows)
	if len(ms) == 1 {
		ui.Warn("Установлен один модуль — память работает в одноканальном режиме (медленнее в играх/iGPU). Второй такой же модуль даст двухканал.")
	}
	if slow {
		ui.Warn("Память работает медленнее номинала — проверьте XMP/EXPO в BIOS (если ноутбук это позволяет).")
	}
	var bugs []struct{ Time, Text string }
	if err := win.PSJSON(60*time.Second, `
$r=@(Get-WinEvent -FilterHashtable @{LogName='System';ProviderName='Microsoft-Windows-WER-SystemErrorReporting';Id=1001;StartTime=(Get-Date).AddDays(-60)} -ErrorAction SilentlyContinue |
 ForEach-Object {[pscustomobject]@{Time=$_.TimeCreated.ToString('yyyy-MM-dd HH:mm');Text=($_.Message -split [char]10)[0]}})
ConvertTo-Json -InputObject $r -Compress`, &bugs); err == nil && len(bugs) > 0 {
		ui.Warn("За 60 дней были синие экраны (%d):", len(bugs))
		for i, b := range bugs {
			if i >= 5 {
				break
			}
			ui.Info("%s  %s", b.Time, ui.Trunc(b.Text, 100))
		}
		ui.Info("Коды 0x1A, 0x4E, 0x50, 0xC2, 0x19 часто означают проблемы с памятью — запустите тест памяти (winfix memory test).")
	}
}

func pagefile() {
	ui.Header("Файл подкачки")
	if !win.IsAdmin() {
		ui.Info("Нужны права администратора: программа перезапустится с запросом UAC — выберите этот пункт ещё раз.")
		win.RequireAdmin("настройка файла подкачки")
		return
	}
	ui.Info("Рекомендация Microsoft и практика: оставить размер на усмотрение системы.")
	if !ui.Confirm("Включить автоматическое управление файлом подкачки?", true) {
		return
	}
	_, err := win.PS(60*time.Second, `$cs=Get-CimInstance Win32_ComputerSystem; Set-CimInstance -InputObject $cs -Property @{AutomaticManagedPagefile=$true}`)
	if err != nil {
		ui.Fail("Не удалось: %v", err)
		return
	}
	ui.OK("Включено. Изменение вступит в силу после перезагрузки.")
}

func memDiag() {
	ui.Header("Тест оперативной памяти (Windows Memory Diagnostic)")
	ui.Info("Тест выполняется при загрузке компьютера (5–30 минут), потом Windows сама загрузится.")
	ui.Info("Результат появится уведомлением; позже можно посмотреть здесь же (команда «Результат последнего теста памяти»).")
	if !ui.Confirm("Запустить мастер mdsched сейчас?", false) {
		return
	}
	win.Run(10*time.Second, "mdsched.exe")
}

func memDiagResult() {
	ui.Header("Результат последнего теста памяти")
	out, err := win.PS(60*time.Second, `
$e=Get-WinEvent -FilterHashtable @{LogName='System';ProviderName='Microsoft-Windows-MemoryDiagnostics-Results'} -MaxEvents 3 -ErrorAction SilentlyContinue
if(-not $e){ 'Результатов нет — тест ещё не запускался.' } else { $e | ForEach-Object { "$($_.TimeCreated.ToString('yyyy-MM-dd HH:mm'))  $($_.Message)" } }`)
	if err != nil {
		ui.Fail("%v", err)
		return
	}
	ui.Info("%s", out)
}

func wslLimit() {
	ui.Header("Ограничение памяти WSL2 / Docker Desktop")
	path := filepath.Join(os.Getenv("USERPROFILE"), ".wslconfig")
	if b, err := os.ReadFile(path); err == nil {
		ui.Info("Сейчас в %s:\n%s", path, string(b))
	} else {
		ui.Info("Файл %s не найден — WSL2 может занимать до 50%% ОЗУ.", path)
	}
	m := win.Memory()
	rec := int(m.TotalPhys>>30) / 4
	if rec < 4 {
		rec = 4
	}
	s, ok := ui.Ask(fmt.Sprintf("Лимит памяти для WSL2 в ГБ (рекомендуется %d, Enter — отмена):", rec))
	if !ok || s == "" {
		return
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 512 {
		ui.Warn("Некорректное значение.")
		return
	}
	if _, err := os.Stat(path); err == nil {
		bak := path + ".bak-" + time.Now().Format("20060102-150405")
		os.Rename(path, bak)
		ui.Dim("Старый файл сохранён: %s", bak)
	}
	cfg := fmt.Sprintf("[wsl2]\nmemory=%dGB\nswap=2GB\n\n[experimental]\nautoMemoryReclaim=gradual\n", n)
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		ui.Fail("Не записать %s: %v", path, err)
		return
	}
	ui.OK("Записано в %s. Чтобы применить: закройте Docker Desktop и выполните `wsl --shutdown`.", path)
	if ui.Confirm("Выполнить wsl --shutdown сейчас? (остановит все WSL-дистрибутивы и контейнеры)", false) {
		win.Run(60*time.Second, "wsl.exe", "--shutdown")
		ui.OK("WSL остановлен; при следующем запуске лимит будет применён.")
	}
}

var protected = map[string]bool{"system": true, "csrss": true, "wininit": true, "winlogon": true, "services": true, "lsass": true, "smss": true,
	"dwm": true, "svchost": true, "memory compression": true, "registry": true, "fontdrvhost": true}

func killProc() {
	ui.Header("Завершение процесса")
	s, ok := ui.Ask("PID процесса (Enter — отмена):")
	if !ok || s == "" {
		return
	}
	pid, err := strconv.Atoi(s)
	if err != nil {
		ui.Warn("Некорректный PID.")
		return
	}
	name, _ := win.PS(20*time.Second, fmt.Sprintf(`(Get-Process -Id %d -ErrorAction SilentlyContinue).ProcessName`, pid))
	if name == "" {
		ui.Warn("Процесса с таким PID нет.")
		return
	}
	if protected[strings.ToLower(name)] {
		ui.Fail("%s — системный процесс, завершать нельзя.", name)
		return
	}
	if !ui.Confirm(fmt.Sprintf("Завершить %s (PID %d)? Несохранённые данные будут потеряны.", name, pid), false) {
		return
	}
	if _, err := win.PS(20*time.Second, fmt.Sprintf(`Stop-Process -Id %d -Force`, pid)); err != nil {
		ui.Fail("Не удалось: %v", err)
		return
	}
	ui.OK("Процесс завершён.")
}

func all() {
	overview()
	top()
	modules()
}
