package notify

import "fmt"

// UpdateOffer is a newer release the update check found.
type UpdateOffer struct {
	// Version is the newest version, "" when nothing newer exists.
	Version string
	// Current is the running version, URL the release page.
	Current, URL string
}

// evalUpdate announces a newer release once per version. An announcement is
// forgotten when the version it was about is no longer news (the program was
// upgraded), so the next release is announced again. offer nil leaves things as they are.
func (s scope) evalUpdate(offer *UpdateOffer) {
	if offer == nil {
		return
	}
	n, had := s.get(UpdateAvailable)
	if offer.Version == "" {
		if had {
			s.del(UpdateAvailable)
		}
		return
	}
	if had && n.Key == offer.Version {
		return // announced already; a delivery that failed is retried by retry()
	}
	msg := fmt.Sprintf("version %s is available (this is %s)", offer.Version, offer.Current)
	if offer.URL != "" {
		msg += ": " + offer.URL
	}
	_, _ = s.announce(UpdateAvailable, offer.Version, msg)
}
