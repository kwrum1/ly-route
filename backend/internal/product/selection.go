package product

import (
	"errors"
	"fmt"
	"sync"
)

var (
	ErrSelectionImmutable     = errors.New("product selection is immutable")
	ErrSelectionUninitialized = errors.New("product selection is not initialized")
)

type Selection struct {
	mu          sync.RWMutex
	profile     Profile
	initialized bool
}

func NewSelection() *Selection {
	return &Selection{}
}

func (selection *Selection) Initialize(profile Profile) error {
	if err := profile.Validate(); err != nil {
		return fmt.Errorf("initialize product selection: %w", err)
	}

	selection.mu.Lock()
	defer selection.mu.Unlock()
	if selection.initialized {
		return ErrSelectionImmutable
	}
	selection.profile = profile
	selection.initialized = true
	return nil
}

func (selection *Selection) Profile() (Profile, error) {
	selection.mu.RLock()
	defer selection.mu.RUnlock()
	if !selection.initialized {
		return Profile{}, ErrSelectionUninitialized
	}
	return selection.profile, nil
}
