package legacymigration

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	// MaxManifestBytes 限制清洗决议文件大小，避免错误路径或异常文件耗尽内存。
	MaxManifestBytes int64 = 1 << 20
)

// ManifestDigest 是获批 manifest 原始字节的 SHA-256，可直接用于 plan/apply 内容绑定。
type ManifestDigest [sha256.Size]byte

// String 返回固定长度的小写十六进制 SHA-256，不包含 manifest 内容。
func (digest ManifestDigest) String() string {
	return hex.EncodeToString(digest[:])
}

func readManifestFile(path string) ([]byte, ManifestDigest, error) {
	if strings.TrimSpace(path) == "" {
		return nil, ManifestDigest{}, gateError("manifest_path_missing", "必须提供私有清洗 manifest 路径")
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, ManifestDigest{}, gateError("manifest_path_invalid", "无法解析私有清洗 manifest 路径")
	}
	parentFD, err := openManifestParent(filepath.Dir(absolutePath))
	if err != nil {
		return nil, ManifestDigest{}, err
	}
	defer func() { _ = unix.Close(parentFD) }()

	fileFD, err := unix.Openat(
		parentFD,
		filepath.Base(absolutePath),
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, ManifestDigest{}, gateError("manifest_not_regular", "私有清洗 manifest 不得是符号链接")
		}
		return nil, ManifestDigest{}, gateError("manifest_open_failed", "无法安全打开私有清洗 manifest")
	}
	file := os.NewFile(uintptr(fileFD), "approved-manifest")
	if file == nil {
		_ = unix.Close(fileFD)
		return nil, ManifestDigest{}, gateError("manifest_open_failed", "无法安全打开私有清洗 manifest")
	}
	defer func() { _ = file.Close() }()

	var before unix.Stat_t
	if err := unix.Fstat(fileFD, &before); err != nil {
		return nil, ManifestDigest{}, gateError("manifest_stat_failed", "无法核验私有清洗 manifest")
	}
	if err := validateManifestStat(before); err != nil {
		return nil, ManifestDigest{}, err
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxManifestBytes+1))
	if err != nil {
		return nil, ManifestDigest{}, gateError("manifest_read_failed", "无法读取私有清洗 manifest")
	}
	if int64(len(data)) > MaxManifestBytes {
		return nil, ManifestDigest{}, gateError("manifest_too_large", "私有清洗 manifest 超过大小上限")
	}

	var after unix.Stat_t
	if err := unix.Fstat(fileFD, &after); err != nil {
		return nil, ManifestDigest{}, gateError("manifest_stat_failed", "无法复核私有清洗 manifest")
	}
	if manifestStatChanged(before, after) || after.Size != int64(len(data)) {
		return nil, ManifestDigest{}, gateError("manifest_file_changed", "私有清洗 manifest 在加载期间发生变化")
	}
	digest := sha256.Sum256(data)
	return data, ManifestDigest(digest), nil
}

func openManifestParent(path string) (int, error) {
	fd, err := openDirectoryWithoutSymlinks(path)
	if err != nil {
		return -1, gateError("manifest_parent_unsafe", "无法安全打开私有清洗 manifest 父目录")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return -1, gateError("manifest_parent_unsafe", "无法核验私有清洗 manifest 父目录")
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uint32(os.Geteuid()) {
		_ = unix.Close(fd)
		return -1, gateError("manifest_parent_unsafe", "私有清洗 manifest 父目录类型或 owner 不安全")
	}
	if stat.Mode&0o022 != 0 {
		_ = unix.Close(fd)
		return -1, gateError("manifest_parent_permissions_too_open", "私有清洗 manifest 父目录不得向 group 或 other 开放写权限")
	}
	return fd, nil
}

func openDirectoryWithoutSymlinks(path string) (int, error) {
	currentFD, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	clean := filepath.Clean(path)
	components := strings.Split(strings.TrimPrefix(clean, string(filepath.Separator)), string(filepath.Separator))
	for _, component := range components {
		if component == "" || component == "." {
			continue
		}
		nextFD, openErr := unix.Openat(
			currentFD,
			component,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
			0,
		)
		_ = unix.Close(currentFD)
		if openErr != nil {
			return -1, openErr
		}
		currentFD = nextFD
	}
	return currentFD, nil
}

func validateManifestStat(stat unix.Stat_t) error {
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return gateError("manifest_not_regular", "私有清洗 manifest 必须是普通文件")
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return gateError("manifest_owner_mismatch", "私有清洗 manifest 必须由当前执行用户持有")
	}
	if stat.Mode&0o077 != 0 {
		return gateError("manifest_permissions_too_open", "私有清洗 manifest 不得向 group 或 other 开放权限")
	}
	if stat.Size < 0 || stat.Size > MaxManifestBytes {
		return gateError("manifest_too_large", "私有清洗 manifest 超过大小上限")
	}
	return nil
}

func manifestStatChanged(before, after unix.Stat_t) bool {
	return before.Dev != after.Dev ||
		before.Ino != after.Ino ||
		before.Mode != after.Mode ||
		before.Uid != after.Uid ||
		before.Gid != after.Gid ||
		before.Size != after.Size ||
		before.Mtim != after.Mtim ||
		before.Ctim != after.Ctim
}
