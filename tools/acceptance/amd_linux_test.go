//go:build linux

package acceptance

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAMDClientsAndActivity(t *testing.T) {
	dir := t.TempDir()
	write := func(fd, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, fd), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	const pci = "0000:03:00.0"
	const device = "drm-driver: amdgpu\ndrm-pdev: " + pci + "\n"
	write("1", device+"drm-client-id: 42\ndrm-resident-vram: 3 MiB\ndrm-memory-vram: 3 MiB\ndrm-engine-gfx: 80 ns\n")
	write("2", device+"drm-client-id: 42\ndrm-resident-vram: 3 MiB\ndrm-engine-gfx: 80 ns\n")
	write("3", device+"drm-client-id: 43\ndrm-memory-vram: 5 KiB\ndrm-cycles-compute: 100\n")
	write("4", "drm-driver: amdgpu\ndrm-pdev: 0000:05:00.0\ndrm-client-id: 99\n")
	before, err := readAMDClients(dir, pci)
	if err != nil || len(before) != 2 || before["42"].vram != 3<<20 || before["43"].vram != 5<<10 {
		t.Fatalf("AMD clients = %v, %v", before, err)
	}
	if observed, advanced := amdEngineActivity(before, before); !observed || advanced {
		t.Fatalf("unchanged activity = %v, %v", observed, advanced)
	}
	write("1", device+"drm-client-id: 42\ndrm-resident-vram: 3 MiB\ndrm-engine-gfx: 100 ns\n")
	write("3", device+"drm-client-id: 43\ndrm-memory-vram: 5 KiB\ndrm-cycles-compute: 99\n")
	after, err := readAMDClients(dir, pci)
	if observed, advanced := amdEngineActivity(before, after); err != nil || !observed || !advanced {
		t.Fatalf("active engine = %v, %v, %v", observed, advanced, err)
	}
	write("1", device+"drm-client-id: 42\ndrm-resident-vram: 3 MiB\ndrm-engine-gfx: 100 ns\ndrm-engine-compute: 200 ns\n")
	started, err := readAMDClients(dir, pci)
	if observed, advanced := amdEngineActivity(after, started); err != nil || !observed || !advanced {
		t.Fatalf("previously unused engine = %v, %v, %v", observed, advanced, err)
	}
	if observed, advanced := amdEngineActivity(before, nil); observed || advanced {
		t.Fatalf("missing activity = %v, %v", observed, advanced)
	}
	write("5", device+"drm-client-id: 44\n")
	if _, err := readAMDClients(dir, pci); err == nil {
		t.Fatal("missing VRAM was treated as observed")
	}
}
