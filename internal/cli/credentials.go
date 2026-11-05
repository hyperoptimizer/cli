package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/hyperoptimizer/cli/internal/fileio"
	"github.com/zalando/go-keyring"
)

type CredentialSource string

const (
	credentialEnvironment CredentialSource = "environment"
	credentialFile        CredentialSource = "credential_file"
	credentialKeyring     CredentialSource = "keyring"
)

func (source CredentialSource) label() string {
	switch source {
	case credentialEnvironment:
		return "HPO_API_KEY"
	case credentialFile:
		return "credential file"
	case credentialKeyring:
		return "saved login (OS keyring)"
	default:
		return string(source)
	}
}

// CredentialStore keeps OS-keyring access injectable without changing its keys.
type CredentialStore interface {
	Get(service, account string) (string, error)
	Set(service, account, key string) error
	Delete(service, account string) error
}
type systemCredentialStore struct{}

func (systemCredentialStore) Get(service, account string) (string, error) {
	return keyring.Get(service, account)
}
func (systemCredentialStore) Set(service, account, key string) error {
	return keyring.Set(service, account, key)
}
func (systemCredentialStore) Delete(service, account string) error {
	return keyring.Delete(service, account)
}

type environmentKey struct {
	value   string
	present bool
}

func (a *App) environmentCredential() environmentKey {
	value, present := a.Getenv("HPO_API_KEY")
	return environmentKey{value: strings.TrimSpace(value), present: present}
}

func (a *App) credential() (string, CredentialSource, error) {
	env := a.environmentCredential()
	if env.present {
		if env.value == "" {
			return "", credentialEnvironment, failure(3, "HPO_API_KEY is empty. Replace or unset it; saved login will not be used.")
		}
		return env.value, credentialEnvironment, nil
	}
	if a.CredentialFile != "" {
		if runtime.GOOS == "windows" {
			return "", credentialFile, failure(3, "Use the Windows credential store or HPO_API_KEY; protected-file storage is supported on Unix only.")
		}
		stat, err := os.Stat(a.CredentialFile)
		if err != nil {
			return "", credentialFile, failure(3, "Cannot read credential file. Run hpo login --credential-file <path>.")
		}
		if stat.Mode().Perm()&0077 != 0 {
			return "", credentialFile, failure(3, "Credential file must have permissions 0600")
		}
		value, err := os.ReadFile(a.CredentialFile)
		return strings.TrimSpace(string(value)), credentialFile, err
	}
	value, err := a.Store.Get("hyperoptimizer", a.Endpoint)
	if err != nil {
		return "", credentialKeyring, failure(3, "No saved login available. Run hpo login, use --credential-file explicitly, or set HPO_API_KEY.")
	}
	return value, credentialKeyring, nil
}

func (a *App) saveCredential(key string) error {
	if a.CredentialFile != "" {
		if runtime.GOOS == "windows" {
			return failure(3, "Use the Windows credential store or HPO_API_KEY; protected-file storage is supported on Unix only.")
		}
		if err := os.MkdirAll(filepath.Dir(a.CredentialFile), 0700); err != nil {
			return err
		}
		return fileio.WriteProtected(a.CredentialFile, []byte(key))
	}
	if err := a.Store.Set("hyperoptimizer", a.Endpoint, key); err != nil {
		return failure(3, "Could not store login in the OS credential store. Use HPO_API_KEY or explicitly choose --credential-file <path> and run login again.")
	}
	return nil
}
func (a *App) credentialOverrideWarning(login bool) {
	env := a.environmentCredential()
	if !env.present {
		return
	}
	message := "HPO_API_KEY remains set. Its key was not removed or revoked, and hpo will continue using it. Unset HPO_API_KEY to stop using it; revoke the key in Dashboard → API Keys to disable it everywhere."
	if login {
		message = "HPO_API_KEY is set, so hpo will continue using it instead of the credential just saved. Unset HPO_API_KEY to use the saved credential."
	}
	if env.value == "" {
		message = "HPO_API_KEY remains set but empty. Authenticated commands will fail until you replace or unset it."
		if login {
			message = "HPO_API_KEY is set but empty. Authenticated commands will fail until you replace or unset it; the credential just saved will not be used."
		}
	}
	fmt.Fprintln(a.Err, "Warning: "+message)
}

func (a *App) loginSaved(message string) error {
	if err := a.event("authenticated", nil, message); err != nil {
		return err
	}
	a.credentialOverrideWarning(true)
	return nil
}

func (a *App) logout() error {
	removed := false
	if a.CredentialFile != "" {
		if runtime.GOOS == "windows" {
			return failure(3, "Use the Windows credential store or HPO_API_KEY; protected-file storage is supported on Unix only.")
		}
		err := os.Remove(a.CredentialFile)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		removed = err == nil
	} else {
		err := a.Store.Delete("hyperoptimizer", a.Endpoint)
		if err != nil && !errors.Is(err, keyring.ErrNotFound) {
			return failure(3, "Could not remove the saved login from the OS credential store.")
		}
		removed = err == nil
	}
	message := "Local credential removed. Revoke the API key in the dashboard to disable it elsewhere."
	if !removed {
		message = "No saved local credential found. Revoke the API key in the dashboard to disable it elsewhere."
	}
	if err := a.event("logged_out", map[string]any{"localCredentialRemoved": removed}, message); err != nil {
		return err
	}
	a.credentialOverrideWarning(false)
	return nil
}
