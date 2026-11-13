package noaadapter

import "testing"

// TestNewSessionHonoursModelContextLimit pins the contract the host relies on:
// Options.ModelContextLimit becomes the session's window, and leaving it unset
// falls back to the 200K default.
//
// Why it matters upstream: every noa threshold is a PERCENTAGE of the window
// (75% prompt, 95% mechanical truncation). A host that forgets to pass the window
// silently gets 200K, so on a 32K/64K model no threshold ever fires and long
// sessions die at the provider with prompt-too-long — while the host has already
// disabled its own compaction.
func TestNewSessionHonoursModelContextLimit(t *testing.T) {
	cases := []struct {
		name  string
		given int
		want  int
	}{
		{"显式 32K", 32_000, 32_000},
		{"显式 128K", 128_000, 128_000},
		{"未指定回退 200K", 0, defaultContextLimit},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sess, err := newSession(Options{
				ArchiveBaseDir:    t.TempDir(),
				SessionID:         "probe",
				ModelContextLimit: c.given,
			})
			if err != nil {
				t.Fatalf("newSession: %v", err)
			}
			if got := sess.Config().ModelContextLimit; got != c.want {
				t.Fatalf("窗口 = %d，期望 %d", got, c.want)
			}
		})
	}
}
