//go:build unix && !linux

package sqlite

func validateLocalFilesystem(string) error { return nil }
