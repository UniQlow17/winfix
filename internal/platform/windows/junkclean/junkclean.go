//go:build windows

// junkclean — поиск и безопасное удаление системного и пользовательского мусора.
package junkclean

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"winfix/internal/platform/windows/win"
	"winfix/internal/ui"
)

type target struct {
	pattern string        // путь или glob (допускаются %ПЕРЕМЕННЫЕ%)
	minAge  time.Duration // удалять только файлы старше
	exts    []string      // если задано — только эти расширения
	files   bool          // pattern указывает на файлы, а не на каталог
}

type fileEntry struct {
	path string
	size int64
}

type dirEntry struct {
	path string
	mod  time.Time
	age  time.Duration
}

type category struct {
	name     string
	note     string
	optional bool // по умолчанию не выбирается
	admin    bool
	targets  []target
	special  string // recycle | dism
	stopSvc  []string

	files []fileEntry
	dirs  []dirEntry
	size  int64
}

const day = 24 * time.Hour

var envRe = regexp.MustCompile(`%([^%]+)%`)

func expand(s string) string {
	return envRe.ReplaceAllStringFunc(s, func(m string) string {
		if v := os.Getenv(m[1 : len(m)-1]); v != "" {
			return v
		}
		return m
	})
}

func browserTargets() []target {
	var t []target
	for _, b := range []string{`Google\Chrome`, `Microsoft\Edge`, `Yandex\YandexBrowser`, `BraveSoftware\Brave-Browser`, `Vivaldi`} {
		base := `%LOCALAPPDATA%\` + b + `\User Data\`
		for _, sub := range []string{`*\Cache`, `*\Code Cache`, `*\GPUCache`, `*\Service Worker\CacheStorage`, `ShaderCache`, `GrShaderCache`, `GraphiteDawnCache`} {
			t = append(t, target{pattern: base + sub})
		}
	}
	t = append(t, target{pattern: `%LOCALAPPDATA%\Mozilla\Firefox\Profiles\*\cache2`})
	t = append(t, target{pattern: `%APPDATA%\Opera Software\Opera Stable\Cache`}, target{pattern: `%APPDATA%\Opera Software\Opera Stable\Code Cache`})
	return t
}

func electronTargets(app string) []target {
	var t []target
	for _, sub := range []string{"Cache", "Code Cache", "GPUCache", "CachedData", "CachedExtensionVSIXs"} {
		t = append(t, target{pattern: `%APPDATA%\` + app + `\` + sub})
	}
	return t
}

func categories() []*category {
	return []*category{
		{name: "Временные файлы пользователя", note: "%TEMP% (старше 1 дня), INetCache",
			targets: []target{{pattern: `%TEMP%`, minAge: day}, {pattern: `%LOCALAPPDATA%\Microsoft\Windows\INetCache`, minAge: day}}},
		{name: "Временные файлы Windows", note: `C:\Windows\Temp (старше 1 дня)`, admin: true,
			targets: []target{{pattern: `%SystemRoot%\Temp`, minAge: day}}},
		{name: "Кэш Центра обновления Windows", note: "SoftwareDistribution\\Download (службы wuauserv/bits на время остановятся)", admin: true,
			stopSvc: []string{"wuauserv", "bits"},
			targets: []target{{pattern: `%SystemRoot%\SoftwareDistribution\Download`}, {pattern: `%SystemRoot%\SoftwareDistribution\DeliveryOptimization`}}},
		{name: "Кэш оптимизации доставки", note: "Delivery Optimization cache", admin: true,
			targets: []target{{pattern: `%ProgramData%\Microsoft\Windows\DeliveryOptimization\Cache`},
				{pattern: `%SystemRoot%\ServiceProfiles\NetworkService\AppData\Local\Microsoft\Windows\DeliveryOptimization\Cache`}}},
		{name: "Дампы сбоев и отчёты об ошибках", note: "CrashDumps, Minidump, MEMORY.DMP, WER", admin: true,
			targets: []target{{pattern: `%LOCALAPPDATA%\CrashDumps`}, {pattern: `%SystemRoot%\Minidump`},
				{pattern: `%SystemRoot%\MEMORY.DMP`, files: true}, {pattern: `%SystemRoot%\LiveKernelReports`, exts: []string{".dmp"}},
				{pattern: `%ProgramData%\Microsoft\Windows\WER\ReportArchive`}, {pattern: `%ProgramData%\Microsoft\Windows\WER\ReportQueue`},
				{pattern: `%ProgramData%\Microsoft\Windows\WER\Temp`}, {pattern: `%LOCALAPPDATA%\Microsoft\Windows\WER`}}},
		{name: "Старые журналы Windows", note: "C:\\Windows\\Logs: .log/.etl/.cab старше 30 дней", admin: true,
			targets: []target{{pattern: `%SystemRoot%\Logs`, minAge: 30 * day, exts: []string{".log", ".etl", ".cab"}}}},
		{name: "Кэш эскизов и значков", note: "thumbcache/iconcache (занятые проводником файлы будут пропущены)",
			targets: []target{{pattern: `%LOCALAPPDATA%\Microsoft\Windows\Explorer\thumbcache_*.db`, files: true},
				{pattern: `%LOCALAPPDATA%\Microsoft\Windows\Explorer\iconcache_*.db`, files: true}}},
		{name: "Кэши браузеров", note: "Chrome, Edge, Yandex, Brave, Vivaldi, Opera, Firefox (куки и пароли не трогаются; закройте браузеры)",
			targets: browserTargets()},
		{name: "Кэши приложений", note: "Discord, VS Code, Cursor, Steam HTML-кэш",
			targets: append(append(append([]target{}, electronTargets("discord")...), append(electronTargets("Code"), electronTargets("Cursor")...)...),
				target{pattern: `%LOCALAPPDATA%\Steam\htmlcache`})},
		{name: "Корзина", note: "все диски", special: "recycle"},
		{name: "Кэши разработчика", note: "npm, pip, yarn, go-build, NuGet, cargo registry (скачается заново при сборке)", optional: true,
			targets: []target{{pattern: `%LOCALAPPDATA%\npm-cache`}, {pattern: `%LOCALAPPDATA%\pip\Cache`}, {pattern: `%LOCALAPPDATA%\Yarn\Cache`},
				{pattern: `%LOCALAPPDATA%\go-build`}, {pattern: `%LOCALAPPDATA%\NuGet\v3-cache`}, {pattern: `%USERPROFILE%\.cargo\registry\cache`}}},
		{name: "Шейдер-кэш видеокарты", note: "NVIDIA DX/GL, D3DSCache — первый запуск игр может быть дольше", optional: true,
			targets: []target{{pattern: `%LOCALAPPDATA%\NVIDIA\DXCache`}, {pattern: `%LOCALAPPDATA%\NVIDIA\GLCache`}, {pattern: `%LOCALAPPDATA%\D3DSCache`},
				{pattern: `%LOCALAPPDATA%\AMD\DxCache`}}},
		{name: "Хранилище компонентов Windows (DISM)", note: "StartComponentCleanup — долго, размер заранее неизвестен", optional: true, admin: true, special: "dism"},
	}
}

var seen = map[string]bool{}

func blocked(p string) bool {
	l := strings.ToLower(filepath.Clean(p))
	bad := []string{strings.ToLower(os.Getenv("SystemRoot")), strings.ToLower(os.Getenv("USERPROFILE")), strings.ToLower(os.Getenv("ProgramData")),
		strings.ToLower(os.Getenv("ProgramFiles")), strings.ToLower(os.Getenv("ProgramFiles(x86)")), strings.ToLower(filepath.Join(os.Getenv("SystemDrive")+`\`, "users"))}
	for _, b := range bad {
		if b != "" && l == b {
			return true
		}
	}
	return len(strings.Split(strings.Trim(l, `\`), `\`)) < 3
}

func hasExt(p string, exts []string) bool {
	if len(exts) == 0 {
		return true
	}
	e := strings.ToLower(filepath.Ext(p))
	for _, x := range exts {
		if e == x {
			return true
		}
	}
	return false
}

func scan(c *category) {
	c.files, c.dirs, c.size = nil, nil, 0
	now := time.Now()
	switch c.special {
	case "recycle":
		for _, d := range win.FixedDrives() {
			root := filepath.Join(d+`\`, "$Recycle.Bin")
			filepath.WalkDir(root, func(p string, de fs.DirEntry, err error) error {
				if err != nil || de.IsDir() {
					return nil
				}
				if i, e := de.Info(); e == nil {
					c.files = append(c.files, fileEntry{p, i.Size()})
					c.size += i.Size()
				}
				return nil
			})
		}
		return
	case "dism":
		return
	}
	for _, t := range c.targets {
		matches, _ := filepath.Glob(expand(t.pattern))
		for _, m := range matches {
			if blocked(m) {
				continue
			}
			st, err := os.Lstat(m)
			if err != nil {
				continue
			}
			add := func(p string, info fs.FileInfo) {
				key := strings.ToLower(p)
				if seen[key] {
					return
				}
				if t.minAge > 0 && now.Sub(info.ModTime()) < t.minAge {
					return
				}
				if !hasExt(p, t.exts) {
					return
				}
				seen[key] = true
				c.files = append(c.files, fileEntry{p, info.Size()})
				c.size += info.Size()
			}
			if !st.IsDir() {
				add(m, st)
				continue
			}
			filepath.WalkDir(m, func(p string, de fs.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				info, e := de.Info()
				if e != nil {
					return nil
				}
				if de.IsDir() {
					if p != m {
						c.dirs = append(c.dirs, dirEntry{p, info.ModTime(), t.minAge})
					}
					return nil
				}
				add(p, info)
				return nil
			})
		}
	}
}

func isAbsolutelyUnder(p string) bool { return !blocked(p) }

func clean(c *category) (freed int64, removed, failed int) {
	switch c.special {
	case "recycle":
		if _, err := win.PS(2*time.Minute, `Clear-RecycleBin -Force -ErrorAction SilentlyContinue`); err != nil {
			ui.Warn("Очистка корзины: %v", err)
		}
		return c.size, len(c.files), 0
	case "dism":
		code, err := win.Stream("Dism.exe", "/Online", "/Cleanup-Image", "/StartComponentCleanup")
		if err != nil || code != 0 {
			ui.Fail("DISM завершился с кодом %d", code)
			return 0, 0, 1
		}
		return 0, 1, 0
	}
	var stopped []string
	if win.IsAdmin() {
		for _, s := range c.stopSvc {
			if out, _ := win.PS(30*time.Second, fmt.Sprintf(`$s=Get-Service %s -ErrorAction SilentlyContinue; if($s -and $s.Status -eq 'Running'){Stop-Service %s -Force -ErrorAction SilentlyContinue; 'stopped'}`, s, s)); strings.Contains(out, "stopped") {
				stopped = append(stopped, s)
			}
		}
	}
	for _, f := range c.files {
		if !isAbsolutelyUnder(f.path) {
			continue
		}
		err := os.Remove(f.path)
		if err != nil {
			os.Chmod(f.path, 0o666)
			err = os.Remove(f.path)
		}
		if err != nil {
			failed++
			continue
		}
		freed += f.size
		removed++
	}
	sort.Slice(c.dirs, func(i, j int) bool { return len(c.dirs[i].path) > len(c.dirs[j].path) })
	for _, d := range c.dirs {
		if d.age > 0 && time.Since(d.mod) < d.age {
			continue
		}
		os.Remove(d.path) // удалится только если пуст
	}
	for _, s := range stopped {
		win.PS(30*time.Second, fmt.Sprintf(`Start-Service %s -ErrorAction SilentlyContinue`, s))
	}
	return
}

func parseSelection(s string, n int) []int {
	set := map[int]bool{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if a, b, ok := strings.Cut(part, "-"); ok {
			x, e1 := strconv.Atoi(strings.TrimSpace(a))
			y, e2 := strconv.Atoi(strings.TrimSpace(b))
			if e1 == nil && e2 == nil {
				for i := x; i <= y; i++ {
					if i >= 1 && i <= n {
						set[i] = true
					}
				}
			}
			continue
		}
		if x, err := strconv.Atoi(part); err == nil && x >= 1 && x <= n {
			set[x] = true
		}
	}
	var res []int
	for k := range set {
		res = append(res, k)
	}
	sort.Ints(res)
	return res
}

func run(args []string) {
	fs := flag.NewFlagSet("junk", flag.ContinueOnError)
	scanOnly := fs.Bool("scan", false, "только показать, сколько мусора найдено (ничего не удаляется)")
	yes := fs.Bool("yes", false, "очистить рекомендуемые категории без вопросов")
	all := fs.Bool("all", false, "вместе с --yes: включить и необязательные категории")
	noElev := fs.Bool("no-elevate", false, "не запрашивать права администратора")
	if fs.Parse(args) != nil {
		return
	}

	ui.Header("Очистка мусора")
	if !*scanOnly && !*noElev {
		win.RequireAdmin("очистка системных папок")
	}
	if !win.IsAdmin() {
		ui.Warn("Без прав администратора системные категории могут быть найдены не полностью.")
	}
	seen = map[string]bool{}

	cats := categories()
	ui.Step("Сканирую диск…")
	sp := ui.Busy("")
	for i, c := range cats {
		sp.Set(fmt.Sprintf("Сканирую (%d/%d): %s", i+1, len(cats), c.name))
		scan(c)
	}
	sp.Stop()

	rows := [][]string{}
	maxSize := int64(1)
	for _, c := range cats {
		if c.size > maxSize {
			maxSize = c.size
		}
	}
	var total, totalRec int64
	for i, c := range cats {
		sz := ui.FormatBytes(c.size)
		cnt := strconv.Itoa(len(c.files))
		if c.special == "dism" {
			sz, cnt = "—", "—"
		}
		tag := ""
		if c.optional {
			tag = "по желанию"
		}
		rows = append(rows, []string{strconv.Itoa(i + 1), c.name, sz, ui.BarNeutral(float64(c.size)/float64(maxSize), 16), cnt, tag})
		total += c.size
		if !c.optional {
			totalRec += c.size
		}
	}
	ui.Table([]string{"№", "Категория", "Размер", "", "Файлов", ""}, rows)
	ui.Info("")
	ui.Info("Итого найдено: %s (рекомендуемые: %s)", ui.FormatBytes(total), ui.FormatBytes(totalRec))
	if _, err := os.Stat(os.Getenv("SystemDrive") + `\Windows.old`); err == nil {
		ui.Warn(`Найден Windows.old — удаляйте через Параметры → Система → Память → Временные файлы.`)
	}
	if *scanOnly {
		return
	}

	var picked []int
	if *yes {
		for i, c := range cats {
			if !c.optional || *all {
				picked = append(picked, i+1)
			}
		}
	} else {
		ui.Info("")
		ui.Info("Введите номера через запятую (1,3,5-7), 'a' — все рекомендуемые, 'all' — вообще все, Enter — выход.")
		s, ok := ui.Ask("Что очистить?")
		if !ok || s == "" || strings.EqualFold(s, "q") {
			return
		}
		switch strings.ToLower(s) {
		case "a":
			for i, c := range cats {
				if !c.optional {
					picked = append(picked, i+1)
				}
			}
		case "all":
			for i := range cats {
				picked = append(picked, i+1)
			}
		default:
			picked = parseSelection(s, len(cats))
		}
	}
	if len(picked) == 0 {
		ui.Warn("Ничего не выбрано.")
		return
	}
	var sel int64
	for _, n := range picked {
		sel += cats[n-1].size
		ui.Info("• %s", cats[n-1].name)
	}
	if !*yes && !ui.Confirm(fmt.Sprintf("Удалить выбранное (≈ %s)?", ui.FormatBytes(sel)), false) {
		return
	}

	var freed int64
	var removed, failed int
	for _, n := range picked {
		c := cats[n-1]
		ui.Step("Очищаю: %s", c.name)
		f, r, bad := clean(c)
		freed += f
		removed += r
		failed += bad
		ui.Dim("освобождено %s, удалено %d, пропущено (занято/нет доступа) %d", ui.FormatBytes(f), r, bad)
	}
	ui.Header("Готово")
	ui.OK("Освобождено: %s (удалено файлов: %d, пропущено: %d)", ui.FormatBytes(freed), removed, failed)
}
