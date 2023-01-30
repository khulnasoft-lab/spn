package navigator

import (
	"context"

	"github.com/safing/portbase/log"
	"github.com/safing/portmaster/intel"
	"github.com/safing/portmaster/profile/endpoints"
	"github.com/safing/spn/hub"
)

// HubType is the usage type of a Hub in routing.
type HubType uint8

// Hub Types.
const (
	HomeHub HubType = iota
	TransitHub
	DestinationHub
)

// Options holds configuration options for operations with the Map.
type Options struct { //nolint:maligned
	// Regard holds required States. Only Hubs where all of these are present
	// will taken into account for the operation. If NoDefaults is not set, a
	// basic set of desirable states is added automatically.
	Regard PinState

	// Disregard holds disqualifying States. Only Hubs where none of these are
	// present will be taken into account for the operation. If NoDefaults is not
	// set, a basic set of undesirable states is added automatically.
	Disregard PinState

	// HubPolicies is a collecion of endpoint lists that Hubs must pass in order
	// to be taken into account for the operation.
	HubPolicies []endpoints.Endpoints

	// CheckHubEntryPolicyWith provides an entity that must match the Hubs entry
	// policy in order to be taken into account for the operation.
	CheckHubEntryPolicyWith *intel.Entity

	// CheckHubExitPolicyWith provides an entity that must match the Hubs exit
	// policy in order to be taken into account for the operation.
	CheckHubExitPolicyWith *intel.Entity

	// RequireVerifiedOwners specifies which verified owners are allowed to be used.
	// If the list is empty, all owners are allowed.
	RequireVerifiedOwners []string

	// NoDefaults declares whether default and recommended Regard and Disregard states should not be used.
	NoDefaults bool

	// RequireTrustedDestinationHubs declares whether only Destination Hubs that have the Trusted state should be used.
	RequireTrustedDestinationHubs bool

	// RoutingProfile defines the algorithm to use to find a route.
	RoutingProfile string
}

// Copy returns a shallow copy of the Options.
func (o *Options) Copy() *Options {
	return &Options{
		Regard:                        o.Regard,
		Disregard:                     o.Disregard,
		HubPolicies:                   o.HubPolicies,
		CheckHubEntryPolicyWith:       o.CheckHubEntryPolicyWith,
		CheckHubExitPolicyWith:        o.CheckHubExitPolicyWith,
		NoDefaults:                    o.NoDefaults,
		RequireTrustedDestinationHubs: o.RequireTrustedDestinationHubs,
		RoutingProfile:                o.RoutingProfile,
	}
}

// PinMatcher is a stateful matching function generated by Options.
type PinMatcher func(pin *Pin) bool

// DefaultOptions returns the default options for this Map.
func (m *Map) DefaultOptions() *Options {
	m.Lock()
	defer m.Unlock()

	return m.defaultOptions()
}

func (m *Map) defaultOptions() *Options {
	opts := &Options{
		RoutingProfile: DefaultRoutingProfileID,
	}

	return opts
}

// HubPoliciesAreSet returns whether any hub policies are set and non-empty.
func (o *Options) HubPoliciesAreSet() bool {
	for _, policy := range o.HubPolicies {
		if policy.IsSet() {
			return true
		}
	}
	return false
}

// Matcher generates a PinMatcher based on the Options.
func (o *Options) Matcher(hubType HubType, hubIntel *hub.Intel) PinMatcher {
	// Compile states to regard and disregard.
	regard := o.Regard
	disregard := o.Disregard

	// Add default states.
	if !o.NoDefaults {
		// Add default States.
		regard = regard.Add(StateSummaryRegard)
		disregard = disregard.Add(StateSummaryDisregard)

		// Add type based Advisories.
		switch hubType {
		case HomeHub:
			// Home Hubs don't need to be reachable and don't need keys ready to be used.
			regard = regard.Remove(StateReachable)
			regard = regard.Remove(StateActive)
			disregard = disregard.Add(StateUsageAsHomeDiscouraged)
		case TransitHub:
			// Transit Hubs get no additional states.
		case DestinationHub:
			disregard = disregard.Add(StateUsageAsDestinationDiscouraged)
			disregard = disregard.Add(StateConnectivityIssues)
		}
	}

	// Add Trusted requirement for Destination Hubs.
	if o.RequireTrustedDestinationHubs && hubType == DestinationHub {
		regard |= StateTrusted
	}

	// Add intel policies.
	hubPolicies := o.HubPolicies
	if hubIntel != nil && hubIntel.Parsed() != nil {
		switch hubType {
		case HomeHub:
			hubPolicies = append(hubPolicies, hubIntel.Parsed().HubAdvisory, hubIntel.Parsed().HomeHubAdvisory)
		case TransitHub:
			hubPolicies = append(hubPolicies, hubIntel.Parsed().HubAdvisory)
		case DestinationHub:
			hubPolicies = append(hubPolicies, hubIntel.Parsed().HubAdvisory, hubIntel.Parsed().DestinationHubAdvisory)
		}
	}

	// Add entry/exit policiy checks.
	checkHubEntryPolicyWith := o.CheckHubEntryPolicyWith
	checkHubExitPolicyWith := o.CheckHubExitPolicyWith

	return func(pin *Pin) bool {
		// Check required Pin States.
		if !pin.State.Has(regard) || pin.State.HasAnyOf(disregard) {
			return false
		}

		// Check verified owners.
		if len(o.RequireVerifiedOwners) > 0 {
			// Check if Pin has a verified owner at all.
			if pin.VerifiedOwner == "" {
				return false
			}

			// Check if verified owner is in the list.
			inList := false
			for _, allowed := range o.RequireVerifiedOwners {
				if pin.VerifiedOwner == allowed {
					inList = true
					break
				}
			}

			// Pin does not have a verified owner from the allowed list.
			if !inList {
				return false
			}
		}

		// Check policies.
	policyCheck:
		for _, policy := range hubPolicies {
			// Check if policy is set.
			if !policy.IsSet() {
				continue
			}

			// Check if policy matches.
			result, reason := policy.MatchMulti(context.TODO(), pin.EntityV4, pin.EntityV6)
			switch result {
			case endpoints.NoMatch:
				// Continue with check.
			case endpoints.MatchError:
				log.Warningf("spn/navigator: failed to match policy: %s", reason)
				// Continue with check for now.
				// TODO: Rethink how to do this. If eg. the geoip database has a
				// problem, then no Hub will match. For now, just continue to the
				// next rule set. Not optimal, but fail safe.
			case endpoints.Denied:
				// Explicitly denied, abort immediately.
				return false
			case endpoints.Permitted:
				// Explicitly allowed, abort check and continue.
				break policyCheck
			}
		}

		// Check entry/exit policies.
		if checkHubEntryPolicyWith != nil &&
			endpointListMatch(pin.Hub.Info.EntryPolicy(), checkHubEntryPolicyWith) == endpoints.Denied {
			// Hub does not allow entry from the given entity.
			return false
		}
		if checkHubExitPolicyWith != nil &&
			endpointListMatch(pin.Hub.Info.EntryPolicy(), checkHubExitPolicyWith) == endpoints.Denied {
			// Hub does not allow exit to the given entity.
			return false
		}

		return true // All checks have passed.
	}
}

func endpointListMatch(list endpoints.Endpoints, entity *intel.Entity) endpoints.EPResult {
	// Check if endpoint list and entity are available.
	if !list.IsSet() || entity == nil {
		return endpoints.NoMatch
	}

	// Match and return result only.
	result, _ := list.Match(context.TODO(), entity)
	return result
}
