package dazsecrets

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
)

// ClientConfig explicitly selects provider executables. It is primarily useful
// for isolated integration tests; applications should call NewDefaultClient.
type ClientConfig struct {
	ProviderPath         string
	ProviderID           string
	Timeout              time.Duration
	FallbackProviderPath string
	FallbackProviderID   string
}

type diskConfig struct {
	Version              int    `toml:"version"`
	ProviderPath         string `toml:"provider_path"`
	ProviderID           string `toml:"provider_id"`
	TimeoutMS            uint64 `toml:"timeout_ms"`
	FallbackProviderPath string `toml:"fallback_provider_path"`
	FallbackProviderID   string `toml:"fallback_provider_id"`
}

// LoadDefaultConfig loads only .config/daz-secrets/provider.toml beneath the
// current account's OS-recorded home directory. Environment variables are not
// consulted for discovery.
func LoadDefaultConfig() (ClientConfig, error) {
	home, err := currentUserHome()
	if err != nil {
		return ClientConfig{}, &Error{Code: CodeInvalid}
	}
	path := filepath.Join(home, ".config", "daz-secrets", "provider.toml")
	return loadConfig(path)
}

func currentUserHome() (string, error) {
	account, err := user.Current()
	if err != nil || account.HomeDir == "" || !filepath.IsAbs(account.HomeDir) || filepath.Clean(account.HomeDir) != account.HomeDir {
		return "", &Error{Code: CodeInvalid}
	}
	return account.HomeDir, nil
}

func loadConfig(path string) (ClientConfig, error) {
	if err := validateConfigFile(path); err != nil {
		return ClientConfig{}, err
	}
	var disk diskConfig
	meta, err := toml.DecodeFile(path, &disk)
	if err != nil || len(meta.Undecoded()) != 0 {
		return ClientConfig{}, &Error{Code: CodeInvalid}
	}
	if disk.TimeoutMS > uint64((1<<63-1)/time.Millisecond) {
		return ClientConfig{}, &Error{Code: CodeInvalid}
	}
	config := ClientConfig{ProviderPath: disk.ProviderPath, ProviderID: disk.ProviderID, Timeout: time.Duration(disk.TimeoutMS) * time.Millisecond, FallbackProviderPath: disk.FallbackProviderPath, FallbackProviderID: disk.FallbackProviderID}
	if disk.Version != 1 {
		return ClientConfig{}, &Error{Code: CodeInvalid}
	}
	if err := validateClientConfig(config); err != nil {
		return ClientConfig{}, err
	}
	return config, nil
}

func validateConfigFile(path string) error {
	info, err := validateSecurePath(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		return &Error{Code: CodeInvalid}
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 {
		return &Error{Code: CodeDenied}
	}
	return nil
}

func validateClientConfig(config ClientConfig) error {
	if !validateName(config.ProviderID) || config.Timeout <= 0 {
		return &Error{Code: CodeInvalid}
	}
	if err := validateProvider(config.ProviderPath); err != nil {
		return err
	}
	if (config.FallbackProviderPath == "") != (config.FallbackProviderID == "") || (config.FallbackProviderID != "" && !validateName(config.FallbackProviderID)) {
		return &Error{Code: CodeInvalid}
	}
	if config.FallbackProviderPath != "" {
		return validateProvider(config.FallbackProviderPath)
	}
	return nil
}

func validateProvider(path string) error {
	info, err := validateSecurePath(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o111 == 0 || info.Mode().Perm()&0o022 != 0 {
		return &Error{Code: CodeDenied}
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 || (stat.Uid != uint32(os.Getuid()) && stat.Uid != 0) {
		return &Error{Code: CodeDenied}
	}
	return nil
}

// validateSecurePath rejects aliasing and replaceable components before a file
// is trusted. A root-owned sticky directory such as /tmp is safe to traverse:
// sticky-bit ownership rules prevent another user from replacing this user's
// child entry. Other group/world-writable ancestors fail closed.
func validateSecurePath(path string) (os.FileInfo, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, &Error{Code: CodeInvalid}
	}
	components := splitAbsolutePath(path)
	current := string(filepath.Separator)
	for index, component := range components {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return nil, wrapCode(CodeUnavailable, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, &Error{Code: CodeDenied}
		}
		if index == len(components)-1 {
			return info, nil
		}
		if !info.IsDir() || !secureDirectory(info) {
			return nil, &Error{Code: CodeDenied}
		}
	}
	return nil, &Error{Code: CodeInvalid}
}

func splitAbsolutePath(path string) []string {
	volume := filepath.VolumeName(path)
	remainder := path[len(volume):]
	return strings.Split(strings.TrimPrefix(remainder, string(filepath.Separator)), string(filepath.Separator))
}

func secureDirectory(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (stat.Uid != 0 && stat.Uid != uint32(os.Getuid())) {
		return false
	}
	writable := info.Mode().Perm()&0o022 != 0
	return !writable || (stat.Uid == 0 && info.Mode()&os.ModeSticky != 0)
}
