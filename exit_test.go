package contem_test

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/maxbolgarin/contem"
)

const startModeEnv = "CONTEM_TEST_START_MODE"

// TestMain re-executes the test binary as a helper subprocess when startModeEnv is set.
// Exit codes of Start cannot be asserted in-process: the testing framework turns
// os.Exit(0) into a panic that contem recovers, making such tests pass vacuously.
// Running the helper before m.Run keeps the real os.Exit behavior observable.
func TestMain(m *testing.M) {
	if mode := os.Getenv(startModeEnv); mode != "" {
		runStartHelper(mode)
		// Reaching this line means Start returned instead of exiting itself.
		os.Exit(7)
	}
	os.Exit(m.Run())
}

func runStartHelper(mode string) {
	logger := slog.Default()

	switch mode {
	case "run-error":
		contem.Start(func(ctx contem.Context) error {
			return errors.New("boom")
		}, logger)
	case "run-error-nil-logger":
		contem.Start(func(ctx contem.Context) error {
			return errors.New("boom")
		}, nil)
	case "run-panic":
		contem.Start(func(ctx contem.Context) error {
			panic("boom")
		}, logger)
	case "custom-exit-code":
		var outerErr error
		contem.Start(func(ctx contem.Context) error {
			return errors.New("boom")
		}, logger, contem.Exit(&outerErr, 42))
	case "success-signal":
		go func() {
			time.Sleep(100 * time.Millisecond)
			syscall.Kill(os.Getpid(), syscall.SIGTERM)
		}()
		contem.Start(func(ctx contem.Context) error {
			return nil
		}, logger)
	case "nowait-returns":
		contem.Start(func(ctx contem.Context) error {
			return nil
		}, logger, contem.WithNoWait())
	default:
		os.Exit(90) // unknown mode, fail loudly
	}
}

func TestStartExitCodes(t *testing.T) {
	cases := []struct {
		mode string
		want int
	}{
		{"run-error", 1},
		{"run-error-nil-logger", 1}, // used to hang forever in Wait()
		{"run-panic", 1},
		{"custom-exit-code", 42},
		{"success-signal", 0},
		{"nowait-returns", 7}, // 7 means Start returned to the caller as documented
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.mode, func(t *testing.T) {
			t.Parallel()

			cmd := exec.Command(os.Args[0])
			cmd.Env = append(os.Environ(), startModeEnv+"="+tc.mode)
			var output bytes.Buffer
			cmd.Stdout = &output
			cmd.Stderr = &output

			if err := cmd.Start(); err != nil {
				t.Fatalf("cannot start subprocess: %v", err)
			}

			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()

			select {
			case err := <-done:
				code := 0
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					code = exitErr.ExitCode()
				} else if err != nil {
					t.Fatalf("subprocess failed: %v\noutput:\n%s", err, output.String())
				}
				if code != tc.want {
					t.Errorf("exit code = %d, want %d\noutput:\n%s", code, tc.want, output.String())
				}
			case <-time.After(15 * time.Second):
				cmd.Process.Kill()
				t.Fatalf("subprocess did not exit (Start hangs?)\noutput:\n%s", output.String())
			}
		})
	}
}
