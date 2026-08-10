package postrecording

import (
	"context"
	"errors"
	"os"
	"reflect"
	"runtime"
	"syscall"
	"testing"
	"time"

	recordingapp "github.com/g0ooo0gle/sazanami-dvr/internal/app/recording"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

type commandCall struct {
	path string
	args []string
}

func TestPowerControllerExecutesAllModesWithFixedArguments(t *testing.T) {
	wake := time.Date(2026, 8, 10, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name string
		mode recording.PostRecordingMode
		want [][]string
	}{
		{"standby", recording.PostRecordingStandby, [][]string{{"/rtc", "--mode", "no", "--time", "1786331045"}, {"/systemctl", "--no-ask-password", "suspend"}, {"/rtc", "--mode", "disable"}}},
		{"standby reboot", recording.PostRecordingStandbyReboot, [][]string{{"/rtc", "--mode", "no", "--time", "1786331045"}, {"/systemctl", "--no-ask-password", "suspend"}, {"/systemctl", "--no-ask-password", "reboot"}}},
		{"suspend", recording.PostRecordingSuspend, [][]string{{"/rtc", "--mode", "no", "--time", "1786331045"}, {"/systemctl", "--no-ask-password", "hibernate"}, {"/rtc", "--mode", "disable"}}},
		{"suspend reboot", recording.PostRecordingSuspendReboot, [][]string{{"/rtc", "--mode", "no", "--time", "1786331045"}, {"/systemctl", "--no-ask-password", "hibernate"}, {"/systemctl", "--no-ask-password", "reboot"}}},
		{"shutdown", recording.PostRecordingShutdown, [][]string{{"/rtc", "--mode", "no", "--time", "1786331045"}, {"/systemctl", "--no-ask-password", "poweroff"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls [][]string
			controller := newPowerController("/systemctl", "/rtc", func(_ context.Context, path string, args ...string) error {
				calls = append(calls, append([]string{path}, args...))
				return nil
			})
			if result := controller.Execute(context.Background(), test.mode, &wake); result.Reason != "" || result.CleanupReason != "" {
				t.Fatalf("result=%+v", result)
			}
			if !reflect.DeepEqual(calls, test.want) {
				t.Fatalf("calls=%v want=%v", calls, test.want)
			}
		})
	}
}

func TestPowerControllerFailureAndCancellationReasons(t *testing.T) {
	wake := time.Date(2026, 8, 10, 3, 4, 5, 0, time.UTC)
	for _, test := range []struct {
		name       string
		failCall   int
		cancelCall int
		mode       recording.PostRecordingMode
		wantReason string
		wantClean  string
		wantCalls  int
	}{
		{name: "wake failure", failCall: 1, mode: recording.PostRecordingStandby, wantReason: "post-recording-wake-failed", wantCalls: 1},
		{name: "power failure", failCall: 2, mode: recording.PostRecordingStandby, wantReason: "post-recording-power-failed", wantCalls: 3},
		{name: "reboot failure", failCall: 3, mode: recording.PostRecordingStandbyReboot, wantReason: "post-recording-reboot-failed", wantCalls: 4},
		{name: "clear failure", failCall: 3, mode: recording.PostRecordingStandby, wantClean: "post-recording-wake-clear-failed", wantCalls: 3},
		{name: "cancelled power", cancelCall: 2, mode: recording.PostRecordingStandby, wantReason: "post-recording-power-cancelled", wantCalls: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			controller := newPowerController("/systemctl", "/rtc", func(_ context.Context, _ string, _ ...string) error {
				calls++
				if calls == test.cancelCall {
					cancel()
					return context.Canceled
				}
				if calls == test.failCall {
					return errors.New("fixed failure")
				}
				return nil
			})
			result := controller.Execute(ctx, test.mode, &wake)
			if result.Reason != test.wantReason || result.CleanupReason != test.wantClean || calls != test.wantCalls {
				t.Fatalf("result=%+v calls=%d", result, calls)
			}
		})
	}
}

func TestPowerControllerRejectsUnavailableAndInvalidRequests(t *testing.T) {
	wake := time.Date(2026, 8, 10, 3, 4, 5, 0, time.UTC)
	valid := newPowerController("/systemctl", "/rtc", func(context.Context, string, ...string) error { return nil })
	for _, test := range []struct {
		name       string
		controller *PowerController
		mode       recording.PostRecordingMode
		wake       *time.Time
		want       string
	}{
		{name: "missing systemctl", controller: newPowerController("", "/rtc", runCommand), mode: recording.PostRecordingStandby, want: "post-recording-power-unavailable"},
		{name: "missing rtcwake", controller: newPowerController("/systemctl", "", runCommand), mode: recording.PostRecordingStandby, wake: &wake, want: "post-recording-power-unavailable"},
		{name: "invalid mode", controller: valid, mode: recording.PostRecordingNothing, want: "post-recording-power-unavailable"},
		{name: "nil controller", mode: recording.PostRecordingStandby, want: "post-recording-power-unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := test.controller.Execute(context.Background(), test.mode, test.wake)
			if result.Reason != test.want {
				t.Fatalf("result=%+v", result)
			}
		})
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if result := valid.Execute(cancelled, recording.PostRecordingStandby, nil); result.Reason != "post-recording-power-cancelled" {
		t.Fatalf("result=%+v", result)
	}
	calls := 0
	withoutRTC := newPowerController("/systemctl", "", func(context.Context, string, ...string) error {
		calls++
		return nil
	})
	if result := withoutRTC.Execute(context.Background(), recording.PostRecordingShutdown, nil); result != (recordingapp.PostRecordingPowerResult{}) || calls != 1 {
		t.Fatalf("result=%+v calls=%d", result, calls)
	}
}

func TestRunCommandUsesFixedEnvironmentAndHandlesFailures(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := runCommand(context.Background(), executable, "-test.run=^TestPowerCommandHelper$", "--", "--power-helper", "success"); err != nil {
		t.Fatal(err)
	}
	if err := runCommand(context.Background(), executable, "-test.run=^TestPowerCommandHelper$", "--", "--power-helper", "failure"); err == nil {
		t.Fatal("非0終了が成功扱いになりました")
	}
	if runtime.GOOS != "windows" {
		if err := runCommand(context.Background(), executable, "-test.run=^TestPowerCommandHelper$", "--", "--power-helper", "signal"); err == nil {
			t.Fatal("signal終了が成功扱いになりました")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := runCommand(ctx, executable, "-test.run=^TestPowerCommandHelper$", "--", "--power-helper", "stall"); err == nil {
		t.Fatal("取消したcommandが成功扱いになりました")
	}
}

func TestPowerCommandHelper(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-2] != "--power-helper" {
		return
	}
	if os.Getenv("LANG") != "C" || os.Getenv("LC_ALL") != "C" || os.Getenv("PATH") != "/usr/bin:/bin" {
		os.Exit(20)
	}
	_, _ = os.Stdout.Write(make([]byte, 64*1024))
	_, _ = os.Stderr.Write(make([]byte, 64*1024))
	switch os.Args[len(os.Args)-1] {
	case "success":
		os.Exit(0)
	case "failure":
		os.Exit(7)
	case "signal":
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		time.Sleep(time.Second)
	case "stall":
		time.Sleep(time.Minute)
	}
	os.Exit(21)
}
