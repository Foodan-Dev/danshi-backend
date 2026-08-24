package legacymigration

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestValidateManifestStatRejectsOwnerMismatch(t *testing.T) {
	owner := uint32(os.Geteuid())
	otherOwner := owner + 1
	stat := unix.Stat_t{Mode: unix.S_IFREG | 0o600, Uid: otherOwner, Size: 1}
	if err := validateManifestStat(stat); err == nil || err.Error() != "manifest_owner_mismatch" {
		t.Fatalf("owner 不一致应被拒绝，实际 %v", err)
	}
}

func TestLoadManifestRejectsSymlinkedImmediateParent(t *testing.T) {
	realParent := t.TempDir()
	realPath := filepath.Join(realParent, "manifest.json")
	if err := os.WriteFile(realPath, emptyManifestJSON(), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	linkRoot := t.TempDir()
	linkedParent := filepath.Join(linkRoot, "linked-parent")
	if err := os.Symlink(realParent, linkedParent); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	assertManifestCode(t, filepath.Join(linkedParent, "manifest.json"), "manifest_parent_unsafe")

	realChild := filepath.Join(realParent, "child")
	if err := os.Mkdir(realChild, 0o700); err != nil {
		t.Fatalf("Mkdir child: %v", err)
	}
	childManifest := filepath.Join(realChild, "manifest.json")
	if err := os.WriteFile(childManifest, emptyManifestJSON(), 0o600); err != nil {
		t.Fatalf("WriteFile child: %v", err)
	}
	linkedAncestor := filepath.Join(linkRoot, "linked-ancestor")
	if err := os.Symlink(realParent, linkedAncestor); err != nil {
		t.Fatalf("Symlink ancestor: %v", err)
	}
	assertManifestCode(t, filepath.Join(linkedAncestor, "child", "manifest.json"), "manifest_parent_unsafe")
}

func TestManifestStatChangedDetectsMetadataReplacement(t *testing.T) {
	before := unix.Stat_t{Dev: 1, Ino: 2, Mode: unix.S_IFREG | 0o600, Uid: 3, Gid: 4, Size: 5}
	after := before
	after.Ino++
	if !manifestStatChanged(before, after) {
		t.Fatal("inode 变化必须判定为加载期间文件变化")
	}
	if manifestStatChanged(before, before) {
		t.Fatal("完全相同的 fstat 不应判定为变化")
	}
}

func TestLoadManifestRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.fifo")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("Mkfifo: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := LoadManifest(path)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || err.Error() != "manifest_not_regular" {
			t.Fatalf("FIFO 应在 fstat 门禁被拒绝，实际 %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("FIFO 在类型检查前阻塞打开")
	}
}
