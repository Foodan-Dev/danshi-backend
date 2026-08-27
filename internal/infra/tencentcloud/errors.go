package tencentcloud

import (
	"sort"
	"strings"

	"github.com/Foodan-Dev/danshi-backend/internal/config"
)

const redactedSecret = "<REDACTED>"

type redactedError struct {
	message string
	cause   error
}

func (e *redactedError) Error() string { return e.message }

func (e *redactedError) Unwrap() error { return e.cause }

type knownSecretRedactor struct {
	secrets []string
}

func newKnownSecretRedactor(cfg config.Config) knownSecretRedactor {
	return newSecretRedactor(
		cfg.ModerationCallbackToken,
		cfg.TencentSecretKey,
	)
}

func newSecretRedactor(secrets ...string) knownSecretRedactor {
	values := make([]string, 0, len(secrets))
	seen := make(map[string]struct{}, len(secrets))
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		if _, exists := seen[secret]; exists {
			continue
		}
		seen[secret] = struct{}{}
		values = append(values, secret)
	}
	sort.SliceStable(values, func(i, j int) bool {
		return len(values[i]) > len(values[j])
	})
	return knownSecretRedactor{secrets: values}
}

func (r knownSecretRedactor) redact(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	redacted := message
	for _, secret := range r.secrets {
		redacted = strings.ReplaceAll(redacted, secret, redactedSecret)
	}
	if redacted == message {
		return err
	}
	return &redactedError{message: redacted, cause: err}
}
