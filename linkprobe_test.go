package env

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadLinksLittle builds testdata/linkprobe, a program that loads a
// config through Load and LoadTo, and checks that math/big is not linked:
// env must not keep it reachable for programs that don't use it.
func TestLoadLinksLittle(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go build")
	}
	bin := filepath.Join(t.TempDir(), "linkprobe")
	if out, err := exec.Command("go", "build", "-o", bin, "./testdata/linkprobe").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	out, err := exec.Command("go", "tool", "nm", bin).Output()
	if err != nil {
		t.Fatalf("go tool nm: %v", err)
	}
	for line := range strings.Lines(string(out)) {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		sym := fields[len(fields)-1]
		if strings.HasPrefix(sym, "math/big.") {
			t.Errorf("linked: %s", sym)
		}
	}
}
