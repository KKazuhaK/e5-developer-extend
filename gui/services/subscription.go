package services

import (
	"context"
)

// SubscriptionService reports when the E5 subscription of each tenant ends
// and lets the person refresh it or enter the end date by hand.
type SubscriptionService struct{ b *Backend }

// NewSubscriptionService returns the service.
func NewSubscriptionService(b *Backend) *SubscriptionService { return &SubscriptionService{b} }

// List returns the subscriptions of every tenant as the engine last saw them.
// It makes no network call.
func (s *SubscriptionService) List() ([]TenantSubscriptions, error) {
	return s.b.Ops.Subscriptions("")
}

// Refresh reads the subscriptions of the tenant of the named account now (of
// every tenant when account is empty). A running engine is asked and waited
// for, up to about 75 seconds; the result says when it had not answered yet.
func (s *SubscriptionService) Refresh(account string) (SubscriptionRefresh, error) {
	return s.b.Ops.RefreshSubscriptions(s.b.bgCtx, account)
}

// SetExpires writes the end date (YYYY-MM-DD, "" to remove it) of the tenant of
// the named account, for when the subscription cannot be read. The engine is
// told to reload the configuration.
func (s *SubscriptionService) SetExpires(account, date string) error {
	if err := s.b.Ops.SetSubscriptionExpires(context.Background(), account, date); err != nil {
		return err
	}
	s.b.Ops.ReloadEngine()
	return nil
}
