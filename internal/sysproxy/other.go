//go:build !darwin && !linux && !windows

package sysproxy

func platformName() string { return "unsupported" }

func platformSupported() bool { return false }

func snapshotPlatform() ([]Entry, error) { return nil, ErrUnsupported }

func applyPlatform(string, []Entry) error { return ErrUnsupported }

func restorePlatform([]Entry) error { return ErrUnsupported }
