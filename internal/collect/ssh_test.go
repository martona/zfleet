package collect

import (
	"fmt"
	"os/exec"
	"runtime"
	"testing"
)

// exitWith produces a real *exec.ExitError with the given code.
func exitWith(t *testing.T, code int) error {
	t.Helper()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/c", fmt.Sprintf("exit %d", code))
	} else {
		cmd = exec.Command("sh", "-c", fmt.Sprintf("exit %d", code))
	}
	err := cmd.Run()
	if err == nil {
		t.Fatalf("expected exit %d to fail", code)
	}
	return err
}

// The prettified error must keep the exit code reachable — the pi5
// regression: a decorated "cat: ... No such file" (exit 1) read as
// transport-down because the ExitError was dropped from the chain.
func TestErrorClassification(t *testing.T) {
	s := Ssh{Dest: "testhost", Label: "testhost"}
	cases := []struct {
		code    int
		down    bool
		missing bool
	}{
		{1, false, false},
		{127, false, true},
		{255, true, false},
	}
	for _, c := range cases {
		raw := exitWith(t, c.code)
		for _, err := range []error{raw, s.decorate(raw), &sshError{"host: pretty text", raw}} {
			if got := transportDown(err); got != c.down {
				t.Errorf("exit %d (%T): transportDown = %v, want %v", c.code, err, got, c.down)
			}
			if got := cmdMissing(err); got != c.missing {
				t.Errorf("exit %d (%T): cmdMissing = %v, want %v", c.code, err, got, c.missing)
			}
		}
	}
}
