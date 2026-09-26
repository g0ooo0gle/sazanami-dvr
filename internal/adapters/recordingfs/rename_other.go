//go:build unix && !linux && !darwin

package recordingfs

import "errors"

func renameNoReplace(_, _ string) error {
	return errors.ErrUnsupported
}
