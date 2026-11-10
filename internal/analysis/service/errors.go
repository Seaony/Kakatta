package modelservice

import (
	"context"
	"errors"

	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/settings"
)

type storageError struct {
	business *service.ServiceError
	cause    error
}

func (e *storageError) Error() string   { return e.business.Error() }
func (e *storageError) Unwrap() []error { return []error{e.business, e.cause} }

func storageFailure(err error) error {
	if err == nil || err == context.Canceled || err == context.DeadlineExceeded {
		return err
	}
	var business *service.ServiceError
	if errors.As(err, &business) {
		return &storageError{business: business, cause: err}
	}
	return &storageError{business: &service.ServiceError{Kind: service.ErrorUnavailable,
		Reason: locales.Message{Code: "service.storage_failure", Args: map[string]any{}}, Fields: []settings.Issue{}}, cause: err}
}

func invalidProvider(field string) error {
	reason := locales.Message{Code: "ai.provider_invalid", Args: map[string]any{"field": field}}
	return &service.ServiceError{Kind: service.ErrorInvalid, Reason: reason, Fields: []settings.Issue{{Key: field, Reason: reason}}}
}

func missingProvider() error {
	return &service.ServiceError{Kind: service.ErrorNotFound,
		Reason: locales.Message{Code: "ai.provider_not_found", Args: map[string]any{}}, Fields: []settings.Issue{}}
}

func closedService() error {
	return &service.ServiceError{Kind: service.ErrorUnavailable,
		Reason: locales.Message{Code: "runtime.closed", Args: map[string]any{}}, Fields: []settings.Issue{}}
}
