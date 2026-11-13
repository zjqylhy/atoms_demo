package model

import (
	"errors"
	"time"
)

// ServiceError carries recovery facts without provider response bodies or keys.
type ServiceError struct {
	Status     int
	Message    string
	RetryAfter time.Duration
	Retryable  bool
	Budget     bool
}

func (e *ServiceError) Error() string { return e.Message }
func CanBackup(err error) bool {
	var service *ServiceError
	if errors.As(err, &service) {
		return !service.Budget && (service.Retryable || service.Status == 0 || service.Status == 401 || service.Status == 403)
	}
	var throttle *TPMThrottleError
	return errors.As(err, &throttle)
}
