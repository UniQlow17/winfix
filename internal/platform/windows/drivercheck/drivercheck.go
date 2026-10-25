//go:build windows

// drivercheck — поиск проблемных и устаревших драйверов, установка обновлений
// через Windows Update, резервное копирование драйверов.
package drivercheck

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"winfix/internal/core"
	"winfix/internal/platform/windows/win"
	"winfix/internal/ui"
)

type device struct {
	Class    string `json:"Class"`
	Name     string `json:"Name"`
	Status   string `json:"Status"`
	Problem  string `json:"Problem"`
	Instance string `json:"Instance"`
}

type driver struct {
	Name         string `json:"Name"`
	Manufacturer string `json:"Manufacturer"`
	Version      string `json:"Version"`
	Date         string `json:"Date"`
	Class        string `json:"Class"`
	Signed       bool   `json:"Signed"`
	Inf          string `json:"Inf"`
}

type update struct {
	ID           string `json:"ID"`
	Title        string `json:"Title"`
	Manufacturer string `json:"Manufacturer"`
	Class        string `json:"Class"`
	Date         string `json:"Date"`
	SizeMB       int    `json:"SizeMB"`
	Rev          int    `json:"Rev"`
}

var found []update // результат последнего поиска

func asList[T any](script string, timeout time.Duration) ([]T, error) {
	var res []T
	err := win.PSJSON(timeout, script, &res)
	return res, err
}

// 1. Проблемные устройства
func problemDevices() {
	ui.Header("Проблемные устройства")
	devs, err := asList[device](`
$r=@(Get-PnpDevice -PresentOnly -ErrorAction SilentlyContinue | Where-Object {$_.Status -ne 'OK' -and $_.Status -ne 'Unknown'} |
 ForEach-Object { [pscustomobject]@{Class="$($_.Class)";Name="$($_.FriendlyName)";Status="$($_.Status)";Problem="$($_.Problem)";Instance="$($_.InstanceId)"} })
ConvertTo-Json -InputObject $r -Compress`, 90*time.Second)
	if err != nil {
		ui.Fail("Не удалось получить список устройств: %v", err)
		return
	}
	if len(devs) == 0 {
		ui.OK("Все присутствующие устройства работают нормально.")
		return
	}
	rows := [][]string{}
	for _, d := range devs {
		rows = append(rows, []string{d.Class, ui.Trunc(d.Name, 50), d.Status, d.Problem})
	}
	ui.Table([]string{"Класс", "Устройство", "Статус", "Проблема"}, rows)
	ui.Info("")
	ui.Warn("Найдено проблемных устройств: %d. Подсказки:", len(devs))
	ui.Info("• CM_PROB_FAILED_INSTALL — нет подходящего драйвера: поищите обновление (команда «Поиск обновлений») или драйвер производителя.")
	ui.Info("• CM_PROB_DISABLED — устройство отключено вручную (включить в диспетчере устройств).")
	ui.Info("• CM_PROB_FAILED_ADD / DRIVER_FAILED_LOAD — повреждён драйвер: переустановите его.")

	// Известный безвредный «призрак»: Intel IPF создаёт слоты DTT, под которые у OEM нет драйвера.
	var dtt []device
	for _, d := range devs {
		if strings.HasPrefix(strings.ToUpper(d.Instance), `SWC\VID8086_DTT`) {
			dtt = append(dtt, d)
		}
	}
	if len(dtt) > 0 {
		ui.Info("")
		ui.Info("Устройства Intel Dynamic Tuning Technology (%d) — безвредные пустые слоты: питанием и температурой процессора управляет Intel IPF.", len(dtt))
		ui.Info("Их можно удалить; после перезагрузки или обновления IPF они могут появиться снова.")
		if ui.Confirm("Удалить эти устройства?", false) {
			win.RequireAdmin("удаление устройств")
			n := 0
			for _, d := range dtt {
				if _, err := win.Run(30*time.Second, "pnputil.exe", "/remove-device", d.Instance); err == nil {
					n++
				}
			}
			ui.OK("Удалено: %d из %d", n, len(dtt))
		}
	}
}

// 2. Инвентарь драйверов
func inventory() {
	ui.Header("Инвентарь драйверов (сторонние, не Microsoft)")
	drs, err := asList[driver](`
$r=@(Get-CimInstance Win32_PnPSignedDriver -ErrorAction SilentlyContinue | Where-Object { $_.DeviceName -and $_.Manufacturer -notmatch 'Microsoft' } |
 ForEach-Object { [pscustomobject]@{Name="$($_.DeviceName)";Manufacturer="$($_.Manufacturer)";Version="$($_.DriverVersion)";
  Date=$(if($_.DriverDate){$_.DriverDate.ToString('yyyy-MM-dd')}else{''});Class="$($_.DeviceClass)";Signed=[bool]$_.IsSigned;Inf="$($_.InfName)"} })
ConvertTo-Json -InputObject $r -Compress`, 120*time.Second)
	if err != nil {
		ui.Fail("Не удалось прочитать драйверы: %v", err)
		return
	}
	now := time.Now()
	type row struct {
		d   driver
		age float64
	}
	var old []row
	unsigned := 0
	for _, d := range drs {
		if !d.Signed {
			unsigned++
		}
		t, err := time.Parse("2006-01-02", d.Date)
		if err != nil || t.Year() <= 2009 { // заглушки 1968/2006 — встроенные универсальные драйверы
			continue
		}
		years := now.Sub(t).Hours() / 24 / 365
		if years >= 3 {
			old = append(old, row{d, years})
		}
	}
	sort.Slice(old, func(i, j int) bool { return old[i].age > old[j].age })
	ui.Info("Драйверов сторонних производителей: %d, без цифровой подписи: %d", len(drs), unsigned)
	if len(old) == 0 {
		ui.OK("Драйверов старше 3 лет не найдено.")
		return
	}
	rows := [][]string{}
	for i, r := range old {
		if i >= 25 {
			break
		}
		rows = append(rows, []string{ui.Trunc(r.d.Name, 44), ui.Trunc(r.d.Manufacturer, 22), r.d.Version, r.d.Date, fmt.Sprintf("%.1f г.", r.age)})
	}
	ui.Table([]string{"Устройство", "Производитель", "Версия", "Дата", "Возраст"}, rows)
	if len(old) > 25 {
		ui.Dim("…и ещё %d", len(old)-25)
	}
	ui.Info("")
	ui.Info("Старый драйвер — не обязательно плохой: для чипсета/USB/звука это нормально.")
	ui.Info("Обновляйте в первую очередь видеокарту, сеть, Wi‑Fi, Bluetooth, хранилище.")
}

// 3. Поиск обновлений
func search() {
	ui.Header("Поиск драйверов в Windows Update")
	ui.Step("Запрашиваю Центр обновления (до минуты)…")
	ups, err := asList[update](`
$s=New-Object -ComObject Microsoft.Update.Session
$r=$s.CreateUpdateSearcher().Search("IsInstalled=0 and Type='Driver'")
$o=@(); foreach($u in $r.Updates){ $o += [pscustomobject]@{ID="$($u.Identity.UpdateID)";Rev=[int]$u.Identity.RevisionNumber;Title="$($u.Title)";Manufacturer="$($u.DriverManufacturer)";Class="$($u.DriverClass)";
 Date=$(if($u.DriverVerDate){$u.DriverVerDate.ToString('yyyy-MM-dd')}else{''});SizeMB=[int]($u.MaxDownloadSize/1MB)} }
ConvertTo-Json -InputObject $o -Compress`, 5*time.Minute)
	if err != nil {
		ui.Fail("Поиск не удался: %v", err)
		return
	}
	found = ups
	if len(ups) == 0 {
		ui.OK("Новых драйверов в Windows Update нет.")
		return
	}
	rows := [][]string{}
	for i, u := range ups {
		rows = append(rows, []string{strconv.Itoa(i + 1), ui.Trunc(u.Title, 66), u.Class, u.Date, fmt.Sprintf("%d МБ", u.SizeMB)})
	}
	ui.Table([]string{"№", "Название", "Класс", "Дата", "Размер"}, rows)
	ui.Info("")
	ui.Info("Установить — команда «Установить найденные обновления». Внимание: если в списке есть драйвер СТАРШЕ уже установленного")
	ui.Info("(например, дата 2017 года у сетевой карты 2026 года) — не ставьте его, это откат.")
}

// 4. Установка найденных
func install() {
	ui.Header("Установка драйверов")
	if len(found) == 0 {
		ui.Warn("Сначала выполните поиск (команда «Поиск обновлений») — список пуст.")
		return
	}
	win.RequireAdmin("установка драйверов")
	s, ok := ui.Ask("Номера для установки (1,3 / all / Enter — отмена):")
	if !ok || s == "" {
		return
	}
	var ids []string
	if strings.EqualFold(s, "all") {
		for _, u := range found {
			ids = append(ids, u.ID)
		}
	} else {
		for _, p := range strings.Split(s, ",") {
			if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil && n >= 1 && n <= len(found) {
				ids = append(ids, found[n-1].ID)
			}
		}
	}
	if len(ids) == 0 {
		ui.Warn("Ничего не выбрано.")
		return
	}
	if ui.Confirm("Создать точку восстановления перед установкой?", true) {
		restorePoint()
	}
	if !ui.Confirm(fmt.Sprintf("Установить драйверов: %d?", len(ids)), false) {
		return
	}
	list := "'" + strings.Join(ids, "','") + "'"
	code, err := win.StreamPS(fmt.Sprintf(`
$ids=@(%s)
$s=New-Object -ComObject Microsoft.Update.Session
$r=$s.CreateUpdateSearcher().Search("IsInstalled=0 and Type='Driver'")
$col=New-Object -ComObject Microsoft.Update.UpdateColl
foreach($u in $r.Updates){ if($ids -contains $u.Identity.UpdateID){ $u.AcceptEula(); [void]$col.Add($u); "Выбрано: $($u.Title)" } }
if($col.Count -eq 0){ "Обновления не найдены (изменился список?)"; exit 2 }
"Скачивание…"; $d=$s.CreateUpdateDownloader(); $d.Updates=$col; $dr=$d.Download(); "Результат скачивания: $($dr.ResultCode) (2 = успешно)"
"Установка…"; $i=$s.CreateUpdateInstaller(); $i.Updates=$col; $ir=$i.Install()
for($k=0;$k -lt $col.Count;$k++){ "{0} -> {1}" -f $col.Item($k).Title, $ir.GetUpdateResult($k).ResultCode }
"Код 2 = успешно. Нужна перезагрузка: $($ir.RebootRequired)"`, list))
	if err != nil || code != 0 {
		ui.Fail("Установка завершилась с ошибкой (код %d)", code)
		return
	}
	ui.OK("Готово. Если нужна перезагрузка — перезагрузите компьютер.")
}

func restorePoint() {
	win.RequireAdmin("точка восстановления")
	ui.Step("Создаю точку восстановления…")
	out, err := win.PS(3*time.Minute, `Enable-ComputerRestore -Drive "$env:SystemDrive\" -ErrorAction SilentlyContinue
Checkpoint-Computer -Description "WinFix drivers" -RestorePointType MODIFY_SETTINGS -ErrorAction Stop; 'ok'`)
	if err != nil || !strings.Contains(out, "ok") {
		ui.Warn("Точка восстановления не создана (Windows ограничивает 1 точку в 24 часа или защита системы выключена): %v", err)
		return
	}
	ui.OK("Точка восстановления создана.")
}

// 5. Бэкап драйверов
func backup() {
	ui.Header("Резервная копия драйверов")
	win.RequireAdmin("экспорт драйверов")
	dir := filepath.Join(`D:\dev\winfix\backups`, "drivers-"+time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		ui.Fail("Не создать папку %s: %v", dir, err)
		return
	}
	ui.Step("Экспортирую сторонние драйверы в %s …", dir)
	code, err := win.Stream("pnputil.exe", "/export-driver", "*", dir)
	if err != nil || code != 0 {
		ui.Fail("pnputil завершился с кодом %d", code)
		return
	}
	ui.OK("Готово: %s", dir)
	ui.Info("Восстановление: pnputil /add-driver \"%s\\*.inf\" /subdirs /install", dir)
}

// 6. Призрачные устройства
func ghosts() {
	ui.Header("«Призрачные» устройства (были подключены, сейчас отсутствуют)")
	devs, err := asList[device](`
$r=@(Get-PnpDevice -ErrorAction SilentlyContinue | Where-Object { -not $_.Present -and $_.Class -in 'USB','WPD','Monitor','HIDClass','Bluetooth','Net','Image','Ports','Keyboard','Mouse' } |
 ForEach-Object { [pscustomobject]@{Class="$($_.Class)";Name="$($_.FriendlyName)";Status="$($_.Status)";Problem="";Instance="$($_.InstanceId)"} })
ConvertTo-Json -InputObject $r -Compress`, 2*time.Minute)
	if err != nil {
		ui.Fail("Ошибка: %v", err)
		return
	}
	if len(devs) == 0 {
		ui.OK("Призрачных устройств нет.")
		return
	}
	cnt := map[string]int{}
	for _, d := range devs {
		cnt[d.Class]++
	}
	rows := [][]string{}
	for c, n := range cnt {
		rows = append(rows, []string{c, strconv.Itoa(n)})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i][0] < rows[j][0] })
	ui.Table([]string{"Класс", "Штук"}, rows)
	ui.Info("")
	ui.Info("Это записи о ранее подключавшихся устройствах (флешки, мониторы, адаптеры).")
	ui.Info("Удаление безопасно: при подключении устройства Windows создаст запись заново.")
	if !ui.Confirm(fmt.Sprintf("Удалить %d записей?", len(devs)), false) {
		return
	}
	win.RequireAdmin("удаление устройств")
	removed := 0
	for _, d := range devs {
		if out, err := win.Run(30*time.Second, "pnputil.exe", "/remove-device", d.Instance); err == nil {
			removed++
		} else {
			ui.Dim("не удалось: %s (%s)", ui.Trunc(d.Name, 40), strings.TrimSpace(ui.Trunc(out, 60)))
		}
	}
	ui.OK("Удалено записей: %d из %d", removed, len(devs))
}

func hints() {
	ui.Header("Где брать драйверы вручную")
	ui.Info("• NVIDIA: приложение NVIDIA App или https://www.nvidia.com/drivers")
	ui.Info("• Intel (графика, Wi‑Fi, Bluetooth): Intel Driver & Support Assistant, https://www.intel.com/support")
	ui.Info("• Чипсет, BIOS, EC, DTT/IPF: сайт производителя ноутбука/материнской платы (название производителя и модель — на наклейке или в «Сведения о системе»)")
	ui.Info("• Realtek LAN/звук: сайт производителя ноутбука, а не realtek.com — OEM-версии стабильнее")
	ui.Info("• Ставьте драйверы по одному и делайте точку восстановления (установка и резервная копия это умеют).")
}

func report() {
	problemDevices()
	inventory()
	search()
}

// Tool описывает утилиту для главного меню и командной строки.
func Tool() core.Tool {
	return core.Tool{
		Name:    "drivers",
		Aliases: []string{"drivercheck", "driver"},
		Title:   "Драйверы: поиск и обновление",
		Summary: "Находит проблемные устройства и устаревшие драйверы, ставит обновления через Windows Update, делает резервную копию драйверов.",
		Details: "Показывает устройства с ошибками, драйверы старше 3 лет, ищет и устанавливает драйверы через Windows Update,\n" +
			"делает резервную копию драйверов и точку восстановления. Не трогает BIOS и драйверы чипсета.",
		Commands: []core.Command{
			{Name: "problems", Title: "Проблемные устройства",
				Hint: "Список устройств, которые Windows считает неисправными (жёлтый значок в диспетчере устройств), с кодом проблемы и пояснением. Начните отсюда.",
				Run:  func([]string) { problemDevices() }},
			{Name: "inventory", Title: "Инвентарь драйверов (старые)",
				Hint: "Сторонние драйверы старше 3 лет и число неподписанных. Помогает понять, что стоит обновить в первую очередь: видеокарту, сеть, Wi‑Fi, хранилище.",
				Run:  func([]string) { inventory() }},
			{Name: "search", Title: "Поиск обновлений в Windows Update",
				Hint: "Запрашивает у Центра обновления новые драйверы (до минуты). Ничего не устанавливает — только показывает список.",
				Run:  func([]string) { search() }},
			{Name: "install", Title: "Установить найденные обновления", Admin: true,
				Hint: "Скачивает и ставит драйверы из последнего поиска; сначала предложит точку восстановления. Сначала выполните поиск.",
				Run:  func([]string) { install() }},
			{Name: "backup", Title: "Резервная копия драйверов", Admin: true,
				Hint: "Экспортирует все сторонние драйверы в папку данных WinFix (pnputil). Сделайте перед обновлением или переустановкой Windows — драйверы можно вернуть одной командой.",
				Run:  func([]string) { backup() }},
			{Name: "restore-point", Title: "Точка восстановления системы", Admin: true,
				Hint: "Создаёт точку восстановления Windows, чтобы откатить систему, если новый драйвер вызовет проблемы. Windows разрешает одну точку в сутки.",
				Run:  func([]string) { restorePoint() }},
			{Name: "ghosts", Title: "«Призрачные» устройства", Admin: true,
				Hint: "Записи о давно отключённых устройствах (флешки, мониторы, адаптеры). Удаление безопасно и убирает мусор из диспетчера устройств.",
				Run:  func([]string) { ghosts() }},
			{Name: "report", Title: "Полная проверка (проблемы + инвентарь + поиск)",
				Hint: "Выполняет первые три проверки подряд и ничего не меняет — удобно как быстрый обзор раз в месяц.",
				Run:  func([]string) { report() }},
			{Name: "links", Title: "Где скачать драйверы вручную",
				Hint: "Куда идти за драйверами видеокарты, чипсета, BIOS и сетевой карты, которых нет в Windows Update.",
				Run:  func([]string) { hints() }},
		},
	}
}
