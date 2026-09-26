//go:build unix && !linux && !darwin

package recordingfs

import "errors"

func renameNoReplace(_, _ string) error {
	return errors.New("recordingfs: no-replace rename is not supported on this platform")
}
