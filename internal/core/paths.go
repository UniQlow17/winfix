package core

import (
	"os"
	"path/filepath"
)

// DataDir возвращает папку данных WinFix (журналы, отчёты, резервные копии)
// и создаёт её подпапку sub. Windows: %LOCALAPPDATA%\WinFix.
func DataDir(sub string) string {
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "WinFix", sub)
	os.MkdirAll(dir, 0o755)
	return dir
}
