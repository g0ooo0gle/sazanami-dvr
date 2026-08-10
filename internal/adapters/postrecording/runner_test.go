//go:build unix

package postrecording

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

func TestOpenCreatesOwnerOnlyDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scripts")
	directory, err := Open(path)
	if err != nil || directory.root != path || directory.identity == nil {
		t.Fatalf("directory=%+v err=%v", directory, err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("mode=%v err=%v", info.Mode(), err)
	}
}

func TestValidateRejectsReplacedRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "scripts")
	directory, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	oldRoot := filepath.Join(parent, "old-scripts")
	if err := os.Rename(root, oldRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "replacement.sh")
	writeScript(t, script, "#!/bin/sh\nexit 0\n", 0o700)
	if err := directory.Validate(script); err == nil {
		t.Fatal("差し替えられた許可ディレクトリを受理しました")
	}
}

func TestOpenRejectsSymlinkAndWritableDirectory(t *testing.T) {
	parent := t.TempDir()
	realPath := filepath.Join(parent, "real")
	if err := os.Mkdir(realPath, 0o700); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(parent, "link")
	if err := os.Symlink(realPath, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(symlink); err == nil {
		t.Fatal("symlinkの許可ディレクトリを受理しました")
	}
	if err := os.Chmod(realPath, 0o770); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(realPath); err == nil {
		t.Fatal("他利用者が書き込める許可ディレクトリを受理しました")
	}
}

func TestValidateAcceptsOnlyExecutableRegularFileInsideRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "scripts")
	directory, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	valid := filepath.Join(root, "valid.sh")
	writeScript(t, valid, "#!/bin/sh\nexit 0\n", 0o700)
	if err := directory.Validate(valid); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.sh")
	writeScript(t, outside, "#!/bin/sh\nexit 0\n", 0o700)
	nonExecutable := filepath.Join(root, "plain.sh")
	writeScript(t, nonExecutable, "#!/bin/sh\nexit 0\n", 0o600)
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(child, "link.sh")
	if err := os.Symlink(valid, symlink); err != nil {
		t.Fatal(err)
	}
	unsafeChild := filepath.Join(root, "unsafe")
	if err := os.Mkdir(unsafeChild, 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unsafeChild, 0o770); err != nil {
		t.Fatal(err)
	}
	unsafeChildScript := filepath.Join(unsafeChild, "unsafe.sh")
	writeScript(t, unsafeChildScript, "#!/bin/sh\nexit 0\n", 0o700)
	for name, path := range map[string]string{
		"empty": "", "relative": "valid.sh", "root": root, "outside": outside,
		"common prefix": root + "-other/file.sh", "non executable": nonExecutable, "symlink": symlink,
		"writable child": unsafeChildScript,
	} {
		t.Run(name, func(t *testing.T) {
			if err := directory.Validate(path); err == nil {
				t.Fatalf("path=%q を受理しました", path)
			}
		})
	}
}

func TestRunUsesFixedEnvironmentAndDiscardsOutput(t *testing.T) {
	root := filepath.Join(t.TempDir(), "scripts")
	directory, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	recordingFile := filepath.Join(root, "recording.ts")
	script := filepath.Join(root, "inspect.sh")
	writeScript(t, script, "#!/bin/sh\nset -eu\n[ \"$#\" -eq 0 ]\n[ \"$PATH\" = /usr/bin:/bin ]\n[ \"$SAZANAMI_RECORDING_NUMBER\" = 17 ]\n[ \"$SAZANAMI_RECORDING_STATE\" = SUCCEEDED ]\n[ \"$SAZANAMI_RECORDING_REASON\" = COMPLETED ]\n[ \"$SAZANAMI_RECORDING_FILE\" = "+strconv.Quote(recordingFile)+" ]\n[ -z \"${SHOULD_NOT_EXIST+x}\" ]\nif read value; then exit 8; fi\nprintf ignored\nprintf ignored >&2\n", 0o700)
	t.Setenv("SHOULD_NOT_EXIST", "private-parent-value")
	reason := (Runner{Directory: directory, Timeout: 5 * time.Second}).Run(context.Background(), script, Environment{
		RecordingNumber: 17, RecordingFile: recordingFile,
		State: recording.AttemptSucceeded, Reason: recording.ReasonCompleted,
	})
	if reason != "" {
		t.Fatalf("reason=%q", reason)
	}
}

func TestCancellationStopsChildProcessGroup(t *testing.T) {
	root := filepath.Join(t.TempDir(), "scripts")
	directory, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(root, "child.pid")
	script := filepath.Join(root, "child.sh")
	writeScript(t, script, "#!/bin/sh\n/bin/sleep 30 &\nprintf '%s' \"$!\" > "+strconv.Quote(pidFile)+"\nwait\n", 0o700)
	ctx, cancel := context.WithCancel(context.Background())
	reasonResult := make(chan string, 1)
	go func() {
		reasonResult <- (Runner{Directory: directory, Timeout: 20 * time.Second}).Run(ctx, script, Environment{
			RecordingNumber: 1, RecordingFile: filepath.Join(root, "recording.ts"),
			State: recording.AttemptSucceeded, Reason: recording.ReasonCompleted,
		})
	}()
	startDeadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(pidFile); err == nil {
			break
		}
		if time.Now().After(startDeadline) {
			cancel()
			t.Fatal("child processが開始しませんでした")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if reason := <-reasonResult; reason != ReasonCancelled {
		t.Fatalf("reason=%q", reason)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil || pid < 1 {
		t.Fatalf("pid=%q err=%v", data, err)
	}
	deadline := time.Now().Add(time.Second)
	for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("child process remains: pid=%d err=%v", pid, err)
	}
}

func TestRunReportsSuccessExitTimeoutAndCancellation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "scripts")
	directory, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	environment := Environment{RecordingNumber: 1, RecordingFile: filepath.Join(root, "recording.ts"), State: recording.AttemptPartial,
		Reason: recording.ReasonUserRequestedStop}
	for _, test := range []struct {
		name, body, want string
		timeout          time.Duration
		cancel           bool
	}{
		{name: "success", body: "#!/bin/sh\nexit 0\n"},
		{name: "exit", body: "#!/bin/sh\nexit 7\n", want: ReasonExitFailed},
		{name: "timeout", body: "#!/bin/sh\nexec /bin/sleep 5\n", timeout: 20 * time.Millisecond, want: ReasonTimeout},
		{name: "cancel", body: "#!/bin/sh\nexec /bin/sleep 5\n", cancel: true, want: ReasonCancelled},
	} {
		t.Run(test.name, func(t *testing.T) {
			script := filepath.Join(root, test.name+".sh")
			writeScript(t, script, test.body, 0o700)
			ctx := context.Background()
			var cancel context.CancelFunc
			if test.cancel {
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			timeout := test.timeout
			if timeout == 0 {
				timeout = 10 * time.Second
			}
			if got := (Runner{Directory: directory, Timeout: timeout}).Run(ctx, script, environment); got != test.want {
				t.Fatalf("reason=%q want=%q", got, test.want)
			}
		})
	}
}

func TestRunReportsStartFailureWithoutLeakingPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "scripts")
	directory, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "invalid-executable")
	if err := os.WriteFile(script, []byte("not an executable format"), 0o700); err != nil {
		t.Fatal(err)
	}
	reason := (Runner{Directory: directory, Timeout: 10 * time.Second}).Run(context.Background(), script, Environment{
		RecordingNumber: 1, RecordingFile: filepath.Join(root, "recording.ts"),
		State: recording.AttemptSucceeded, Reason: recording.ReasonCompleted,
	})
	if reason != ReasonStartFailed || strings.Contains(reason, root) || strings.Contains(reason, script) {
		t.Fatalf("reason=%q", reason)
	}
}

func TestRunRejectsChangedScriptAtExecution(t *testing.T) {
	root := filepath.Join(t.TempDir(), "scripts")
	directory, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "changed.sh")
	writeScript(t, script, "#!/bin/sh\nexit 0\n", 0o700)
	if err := directory.Validate(script); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(script); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(script, 0o700); err != nil {
		t.Fatal(err)
	}
	reason := (Runner{Directory: directory, Timeout: time.Second}).Run(context.Background(), script, Environment{
		RecordingNumber: 1, RecordingFile: filepath.Join(root, "recording.ts"),
		State: recording.AttemptSucceeded, Reason: recording.ReasonCompleted,
	})
	if reason != ReasonInvalid {
		t.Fatalf("reason=%q", reason)
	}
}

func writeScript(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if !strings.HasPrefix(body, "#!") {
		t.Fatal("shebangがありません")
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}
