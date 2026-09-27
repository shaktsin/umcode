//go:build !darwin

package computeruse

import (
	"context"
	"errors"
)

type unavailableDriver struct{}

func newDriver() driver { return unavailableDriver{} }
func (unavailableDriver) List(context.Context) ([]App, error) {
	return nil, errors.New("Computer Use is currently available only in the packaged macOS app")
}
func (unavailableDriver) Open(context.Context, Target) (Target, error) {
	return Target{}, errors.New("Computer Use is currently available only in the packaged macOS app")
}
func (unavailableDriver) Inspect(context.Context, Target, string) (State, error) {
	return State{}, errors.New("Computer Use is currently available only in the packaged macOS app")
}
func (unavailableDriver) Act(context.Context, Target, Action) error {
	return errors.New("Computer Use is currently available only in the packaged macOS app")
}
