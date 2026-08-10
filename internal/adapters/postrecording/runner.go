//go:build unix

// Package postrecordingは録画後スクリプトを専用ディレクトリ内に限定して直接実行する。
package postrecording

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

const (
	// DefaultTimeoutは一件の録画後スクリプトを待つ最大時間である。
	DefaultTimeout = 5 * time.Minute

	// ReasonInvalidは実行直前のpathまたは入力検証に失敗したことを表す。
	ReasonInvalid = "post-recording-script-invalid"
	// ReasonStartFailedは検証済みファイルをprocessとして開始できなかったことを表す。
	ReasonStartFailed = "post-recording-script-start-failed"
	// ReasonExitFailedは開始したprocessが非0で終了したことを表す。
	ReasonExitFailed = "post-recording-script-exit-failed"
	// ReasonTimeoutは最大実行時間を超えてprocess groupを終了したことを表す。
	ReasonTimeout = "post-recording-script-timeout"
	// ReasonCancelledはSazanamiの停止に合わせてprocess groupを終了したことを表す。
	ReasonCancelled = "post-recording-script-cancelled"
)

// Directoryは起動時に固定した、所有者だけが変更できるスクリプト保存先である。
type Directory struct {
	root     string
	identity os.FileInfo
}

// Openは絶対pathの専用ディレクトリを0700で用意し、symlinkと他利用者の書込みを拒否する。
func Open(path string) (*Directory, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("postrecording: invalid directory path")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, errors.New("postrecording: create directory")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("postrecording: unsafe directory")
	}
	return &Directory{root: path, identity: info}, nil
}

// Validateはpathが専用ディレクトリ内の実行可能な通常ファイルを指すことを確認する。
// 予約保存時と実行直前の両方で呼び、途中のsymlinkも許可しない。
func (directory *Directory) Validate(path string) error {
	settings := recording.PostRecordingSettings{Script: path}
	if directory == nil || directory.root == "" || directory.identity == nil || path == "" || settings.Validate() != nil ||
		!filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("postrecording: invalid script path")
	}
	rootInfo, err := os.Lstat(directory.root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 || rootInfo.Mode().Perm()&0o077 != 0 ||
		!os.SameFile(directory.identity, rootInfo) {
		return errors.New("postrecording: directory changed")
	}
	relative, err := filepath.Rel(directory.root, path)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return errors.New("postrecording: script outside directory")
	}
	current := directory.root
	parts := strings.Split(relative, string(filepath.Separator))
	for index, part := range parts {
		if part == "" || part == "." || part == ".." {
			return errors.New("postrecording: invalid path component")
		}
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("postrecording: unsafe path component")
		}
		if index < len(parts)-1 {
			if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
				return errors.New("postrecording: unsafe child directory")
			}
			continue
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 || syscall.Access(current, 1) != nil {
			return errors.New("postrecording: script is not executable")
		}
	}
	return nil
}

// Environmentは録画後スクリプトへ渡す、固定名の最小情報である。
type Environment struct {
	RecordingNumber int32
	RecordingFile   string
	State           recording.AttemptState
	Reason          recording.TerminalReason
}

// RunnerはshellとPATH検索を使わず、一件のスクリプトを上限時間内で一度だけ実行する。
type Runner struct {
	Directory *Directory
	Timeout   time.Duration
}

// Runは終了理由だけを返す。空文字は正常終了を表し、pathやprocess出力は返さない。
func (runner Runner) Run(ctx context.Context, script string, environment Environment) string {
	if ctx == nil || runner.Directory.Validate(script) != nil || environment.RecordingNumber < 1 ||
		!filepath.IsAbs(environment.RecordingFile) ||
		(environment.State != recording.AttemptSucceeded && environment.State != recording.AttemptPartial) || environment.Reason == "" {
		return ReasonInvalid
	}
	timeout := runner.Timeout
	if timeout <= 0 || timeout > DefaultTimeout {
		timeout = DefaultTimeout
	}
	runContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(runContext, script)
	command.Env = []string{
		"PATH=/usr/bin:/bin",
		"SAZANAMI_RECORDING_NUMBER=" + strconv.FormatInt(int64(environment.RecordingNumber), 10),
		"SAZANAMI_RECORDING_FILE=" + environment.RecordingFile,
		"SAZANAMI_RECORDING_STATE=" + string(environment.State),
		"SAZANAMI_RECORDING_REASON=" + string(environment.Reason),
	}
	command.Stdin = nil
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	command.WaitDelay = time.Second
	if err := command.Start(); err != nil {
		if errors.Is(runContext.Err(), context.DeadlineExceeded) {
			return ReasonTimeout
		}
		if errors.Is(runContext.Err(), context.Canceled) {
			return ReasonCancelled
		}
		return ReasonStartFailed
	}
	err := command.Wait()
	if errors.Is(runContext.Err(), context.DeadlineExceeded) {
		return ReasonTimeout
	}
	if errors.Is(runContext.Err(), context.Canceled) {
		return ReasonCancelled
	}
	if err != nil {
		return ReasonExitFailed
	}
	return ""
}
