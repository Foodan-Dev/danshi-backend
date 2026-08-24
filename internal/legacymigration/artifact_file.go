package legacymigration

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// MaxExecutionArtifactBytes 限制 dataset/plan artifact 大小，防止错误路径耗尽内存。
const MaxExecutionArtifactBytes int64 = 1 << 20

func readExecutionArtifactFile(path string) ([]byte, error) {
	if strings.TrimSpace(path) == "" {
		return nil, gateError("artifact_path_missing", "必须提供审批 artifact 路径")
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, gateError("artifact_path_invalid", "无法解析审批 artifact 路径")
	}
	parentFD, err := openExecutionArtifactParent(filepath.Dir(absolutePath))
	if err != nil {
		return nil, err
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
			return nil, gateError("artifact_not_regular", "审批 artifact 不得是符号链接")
		}
		return nil, gateError("artifact_open_failed", "无法安全打开审批 artifact")
	}
	file := os.NewFile(uintptr(fileFD), "approved-execution-artifact")
	if file == nil {
		_ = unix.Close(fileFD)
		return nil, gateError("artifact_open_failed", "无法安全打开审批 artifact")
	}
	defer func() { _ = file.Close() }()

	var before unix.Stat_t
	if err := unix.Fstat(fileFD, &before); err != nil {
		return nil, gateError("artifact_stat_failed", "无法核验审批 artifact")
	}
	if err := validateExecutionArtifactStat(before); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxExecutionArtifactBytes+1))
	if err != nil {
		return nil, gateError("artifact_read_failed", "无法读取审批 artifact")
	}
	if int64(len(data)) > MaxExecutionArtifactBytes {
		return nil, gateError("artifact_too_large", "审批 artifact 超过大小上限")
	}
	var after unix.Stat_t
	if err := unix.Fstat(fileFD, &after); err != nil {
		return nil, gateError("artifact_stat_failed", "无法复核审批 artifact")
	}
	if manifestStatChanged(before, after) || after.Size != int64(len(data)) {
		return nil, gateError("artifact_file_changed", "审批 artifact 在加载期间发生变化")
	}
	return data, nil
}

func writeExecutionArtifactFile(path string, data []byte) error {
	if strings.TrimSpace(path) == "" {
		return gateError("artifact_output_path_missing", "必须提供 artifact 输出路径")
	}
	if len(data) == 0 || int64(len(data)) > MaxExecutionArtifactBytes {
		return gateError("artifact_output_invalid", "待写入 artifact 大小无效")
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return gateError("artifact_output_path_invalid", "无法解析 artifact 输出路径")
	}
	parentFD, err := openExecutionArtifactParent(filepath.Dir(absolutePath))
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(parentFD) }()

	base := filepath.Base(absolutePath)
	fileFD, err := unix.Openat(
		parentFD,
		base,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		if errors.Is(err, unix.EEXIST) || errors.Is(err, unix.ELOOP) {
			return gateError("artifact_output_exists", "artifact 输出目标已存在，禁止覆盖")
		}
		return gateError("artifact_output_open_failed", "无法安全创建 artifact 输出文件")
	}
	file := os.NewFile(uintptr(fileFD), "new-execution-artifact")
	if file == nil {
		_ = unix.Close(fileFD)
		_ = unix.Unlinkat(parentFD, base, 0)
		return gateError("artifact_output_open_failed", "无法安全创建 artifact 输出文件")
	}
	written := false
	defer func() {
		_ = file.Close()
		if !written {
			_ = unix.Unlinkat(parentFD, base, 0)
		}
	}()

	if err := unix.Fchmod(fileFD, 0o600); err != nil {
		return gateError("artifact_output_permissions_failed", "无法收紧 artifact 输出文件权限")
	}
	n, err := file.Write(data)
	if err != nil || n != len(data) {
		return gateError("artifact_output_write_failed", "无法完整写入 artifact")
	}
	if err := file.Sync(); err != nil {
		return gateError("artifact_output_sync_failed", "无法持久化 artifact")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fileFD, &stat); err != nil {
		return gateError("artifact_output_stat_failed", "无法复核 artifact 输出文件")
	}
	if err := validateExecutionArtifactStat(stat); err != nil || stat.Size != int64(len(data)) {
		return gateError("artifact_output_stat_failed", "artifact 输出文件属性不安全")
	}
	if err := file.Close(); err != nil {
		return gateError("artifact_output_close_failed", "无法正常结束 artifact 写入")
	}
	if err := unix.Fsync(parentFD); err != nil {
		return gateError("artifact_output_sync_failed", "无法持久化 artifact 目录项")
	}
	written = true
	return nil
}

func openExecutionArtifactParent(path string) (int, error) {
	fd, err := openDirectoryWithoutSymlinks(path)
	if err != nil {
		return -1, gateError("artifact_parent_unsafe", "无法安全打开 artifact 父目录")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return -1, gateError("artifact_parent_unsafe", "无法核验 artifact 父目录")
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uint32(os.Geteuid()) {
		_ = unix.Close(fd)
		return -1, gateError("artifact_parent_unsafe", "artifact 父目录类型或 owner 不安全")
	}
	if stat.Mode&0o022 != 0 {
		_ = unix.Close(fd)
		return -1, gateError("artifact_parent_permissions_too_open", "artifact 父目录不得向 group 或 other 开放写权限")
	}
	return fd, nil
}

func validateExecutionArtifactStat(stat unix.Stat_t) error {
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return gateError("artifact_not_regular", "审批 artifact 必须是普通文件")
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return gateError("artifact_owner_mismatch", "审批 artifact 必须由当前执行用户持有")
	}
	if stat.Mode&0o077 != 0 {
		return gateError("artifact_permissions_too_open", "审批 artifact 不得向 group 或 other 开放权限")
	}
	if stat.Size < 0 || stat.Size > MaxExecutionArtifactBytes {
		return gateError("artifact_too_large", "审批 artifact 超过大小上限")
	}
	return nil
}
