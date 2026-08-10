package postrecording

import (
	"context"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	recordingapp "github.com/g0ooo0gle/sazanami-dvr/internal/app/recording"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

const wakeCleanupTimeout = 10 * time.Second

type commandRunner func(context.Context, string, ...string) error

// PowerControllerは録画終了後のRTC復帰時刻とLinux電源動作を、shellを介さず順番に実行する。
// command pathは生成時に一度だけ解決し、実行時の入力や環境から変更しない。
type PowerController struct {
	systemctl string
	rtcwake   string
	run       commandRunner
}

// NewPowerControllerは現在のPATHから必要なcommandを一度だけ解決する。
// 見つからなくても起動は妨げず、実行要求を固定診断で拒否する。
func NewPowerController() *PowerController {
	return newPowerController(resolveCommand("systemctl"), resolveCommand("rtcwake"), runCommand)
}

func newPowerController(systemctl, rtcwake string, runner commandRunner) *PowerController {
	return &PowerController{systemctl: systemctl, rtcwake: rtcwake, run: runner}
}

func resolveCommand(name string) string {
	path, err := exec.LookPath(name)
	if err != nil || !filepath.IsAbs(path) {
		return ""
	}
	return filepath.Clean(path)
}

func runCommand(ctx context.Context, path string, arguments ...string) error {
	command := exec.CommandContext(ctx, path, arguments...)
	command.Env = []string{"LANG=C", "LC_ALL=C", "PATH=/usr/bin:/bin"}
	command.Stdin = nil
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return command.Run()
}

// Executeは任意のRTC設定後に電源動作を一度だけ実行し、固定診断だけを返す。
func (controller *PowerController) Execute(ctx context.Context, mode recording.PostRecordingMode,
	wakeAt *time.Time,
) recordingapp.PostRecordingPowerResult {
	if ctx == nil || ctx.Err() != nil {
		return recordingapp.PostRecordingPowerResult{Reason: "post-recording-power-cancelled"}
	}
	action, reboot, ok := powerAction(mode)
	if controller == nil || controller.run == nil || controller.systemctl == "" || !ok {
		return recordingapp.PostRecordingPowerResult{Reason: "post-recording-power-unavailable"}
	}
	wakeSet := false
	if wakeAt != nil {
		wakeSecond := wakeAt.UTC().Unix()
		if controller.rtcwake == "" || wakeAt.IsZero() || wakeSecond < 0 {
			return recordingapp.PostRecordingPowerResult{Reason: "post-recording-power-unavailable"}
		}
		if err := controller.run(ctx, controller.rtcwake, "--mode", "no", "--time", strconv.FormatInt(wakeSecond, 10)); err != nil {
			return recordingapp.PostRecordingPowerResult{Reason: commandFailureReason(ctx, "post-recording-wake-failed")}
		}
		wakeSet = true
	}
	if err := controller.run(ctx, controller.systemctl, "--no-ask-password", action); err != nil {
		return recordingapp.PostRecordingPowerResult{
			Reason: commandFailureReason(ctx, "post-recording-power-failed"), CleanupReason: controller.clearWake(ctx, wakeSet),
		}
	}
	if reboot {
		if err := controller.run(ctx, controller.systemctl, "--no-ask-password", "reboot"); err != nil {
			return recordingapp.PostRecordingPowerResult{
				Reason: commandFailureReason(ctx, "post-recording-reboot-failed"), CleanupReason: controller.clearWake(ctx, wakeSet),
			}
		}
		return recordingapp.PostRecordingPowerResult{}
	}
	if action == "suspend" || action == "hibernate" {
		return recordingapp.PostRecordingPowerResult{CleanupReason: controller.clearWake(ctx, wakeSet)}
	}
	return recordingapp.PostRecordingPowerResult{}
}

func (controller *PowerController) clearWake(ctx context.Context, wakeSet bool) string {
	if !wakeSet {
		return ""
	}
	cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), wakeCleanupTimeout)
	defer cancel()
	if err := controller.run(cleanupContext, controller.rtcwake, "--mode", "disable"); err != nil {
		return "post-recording-wake-clear-failed"
	}
	return ""
}

func commandFailureReason(ctx context.Context, fallback string) string {
	if ctx.Err() != nil {
		return "post-recording-power-cancelled"
	}
	return fallback
}

func powerAction(mode recording.PostRecordingMode) (action string, reboot bool, ok bool) {
	switch mode {
	case recording.PostRecordingStandby:
		return "suspend", false, true
	case recording.PostRecordingStandbyReboot:
		return "suspend", true, true
	case recording.PostRecordingSuspend:
		return "hibernate", false, true
	case recording.PostRecordingSuspendReboot:
		return "hibernate", true, true
	case recording.PostRecordingShutdown:
		return "poweroff", false, true
	default:
		return "", false, false
	}
}
