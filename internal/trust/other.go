//go:build !darwin && !linux && !windows

package trust

func platformAvailable() bool { return false }

func platformCheck(*Manager) (Status, error) { return Status{}, ErrUnsupported }

func platformInstall(*Manager, bool) (string, error) { return "", ErrUnsupported }

func platformUninstall(*Manager, bool) error { return ErrUnsupported }
