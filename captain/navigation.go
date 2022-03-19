package captain

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/safing/portbase/log"
	"github.com/safing/portbase/modules"
	"github.com/safing/portmaster/intel"
	"github.com/safing/portmaster/netenv"
	"github.com/safing/portmaster/profile/endpoints"
	"github.com/safing/spn/access"
	"github.com/safing/spn/docks"
	"github.com/safing/spn/hub"
	"github.com/safing/spn/navigator"
	"github.com/safing/spn/terminal"
)

const stopCraneAfterBeingUnsuggestedFor = 6 * time.Hour

// ErrAllHomeHubsExcluded is returned when all available home hubs were excluded.
var ErrAllHomeHubsExcluded = errors.New("all home hubs are excluded")

var (
	homeHubManagerNetworkChangedFlag = netenv.GetNetworkChangedFlag()
	homeHubHealthCheckTickDuration   = 1 * time.Minute
)

func establishHomeHub(ctx context.Context) error {
	// Get own IP.
	locations, ok := netenv.GetInternetLocation()
	if !ok {
		return errors.New("failed to locate own device")
	}
	log.Debugf(
		"spn/captain: looking for new home hub near %s and %s",
		locations.BestV4(),
		locations.BestV6(),
	)

	// Get own entity.
	// Checking the entity against the entry policies is somewhat hit and miss
	// anyway, as the device location is an approximation.
	var myEntity *intel.Entity
	if dl := locations.BestV4(); dl != nil && dl.IP != nil {
		myEntity = &intel.Entity{}
		myEntity.SetIP(dl.IP)
		myEntity.FetchData(ctx)
	} else if dl := locations.BestV6(); dl != nil && dl.IP != nil {
		myEntity = &intel.Entity{}
		myEntity.SetIP(dl.IP)
		myEntity.FetchData(ctx)
	}

	// Get home hub policy for selecting the home hub.
	homePolicy, err := getHomeHubPolicy()
	if err != nil {
		return err
	}

	// Build navigation options for searching for a home hub.
	opts := &navigator.Options{
		HubPolicies:             []endpoints.Endpoints{homePolicy},
		CheckHubEntryPolicyWith: myEntity,
	}

	// Find nearby hubs.
findCandidates:
	candidates, err := navigator.Main.FindNearestHubs(
		locations.BestV4().LocationOrNil(),
		locations.BestV6().LocationOrNil(),
		opts, navigator.HomeHub, 10,
	)
	if err != nil {
		if errors.Is(err, navigator.ErrEmptyMap) {
			// bootstrap to the network!
			err := bootstrapWithUpdates()
			if err != nil {
				return err
			}
			goto findCandidates
		}

		return fmt.Errorf("failed to find nearby hubs: %w", err)
	}

	// Check if any candidates were returned.
	if len(candidates) == 0 && len(homePolicy) > 0 {
		return ErrAllHomeHubsExcluded
	}

	// Try connecting to a hub.
	var tries int
	var candidate *hub.Hub
	for tries, candidate = range candidates {
		err = connectToHomeHub(ctx, candidate)
		if err != nil {
			if errors.Is(err, terminal.ErrStopping) {
				return err
			}
			log.Debugf("spn/captain: failed to connect to %s as new home: %s", candidate, err)
		} else {
			log.Infof("spn/captain: established connection to %s as new home with %d failed tries", candidate, tries)
			return nil
		}
	}
	if err != nil {
		return fmt.Errorf("failed to connect to a new home hub - tried %d hubs: %w", tries+1, err)
	}
	return fmt.Errorf("no home hub candidates available")
}

func connectToHomeHub(ctx context.Context, dst *hub.Hub) error {
	// Set and clean up exceptions.
	setExceptions(dst.Info.IPv4, dst.Info.IPv6)
	defer setExceptions(nil, nil)

	// Connect to hub.
	crane, err := EstablishCrane(ctx, dst)
	if err != nil {
		return err
	}

	// Cleanup connection in case of failure.
	var success bool
	defer func() {
		if !success {
			crane.Stop(nil)
		}
	}()

	// Query all gossip msgs on first connection.
	gossipQuery, tErr := NewGossipQueryOp(crane.Controller)
	if tErr != nil {
		log.Warningf("spn/captain: failed to start initial gossip query: %s", tErr)
	}
	// Wait for gossip query to complete.
	select {
	case <-gossipQuery.ctx.Done():
	case <-ctx.Done():
	}

	// Create communication terminal.
	homeTerminal, initData, tErr := docks.NewLocalCraneTerminal(crane, nil, &terminal.TerminalOpts{}, nil)
	if tErr != nil {
		return tErr.Wrap("failed to create home terminal")
	}
	tErr = crane.EstablishNewTerminal(homeTerminal, initData)
	if tErr != nil {
		return tErr.Wrap("failed to connect home terminal")
	}

	// Authenticate to home hub.
	authOp, tErr := access.AuthorizeToTerminal(homeTerminal)
	if tErr != nil {
		return tErr.Wrap("failed to authorize")
	}
	select {
	case tErr := <-authOp.Ended:
		if !tErr.Is(terminal.ErrExplicitAck) {
			return tErr.Wrap("failed to authenticate to")
		}
	case <-time.After(3 * time.Second):
		return terminal.ErrTimeout.With("timed out waiting for auth to complete")
	case <-ctx.Done():
		return terminal.ErrStopping
	}

	// Set new home on map.
	ok := navigator.Main.SetHome(dst.ID, homeTerminal)
	if !ok {
		return fmt.Errorf("failed to set home hub on map")
	}

	// Assign crane to home hub in order to query it later.
	docks.AssignCrane(crane.ConnectedHub.ID, crane)

	success = true
	return nil
}

func optimizeNetwork(ctx context.Context, task *modules.Task) error {
	if publicIdentity == nil {
		return nil
	}

optimize:
	result, err := navigator.Main.Optimize(nil)
	if err != nil {
		if errors.Is(err, navigator.ErrEmptyMap) {
			// bootstrap to the network!
			err := bootstrapWithUpdates()
			if err != nil {
				return err
			}
			goto optimize
		}

		return err
	}

	// Create any new connections.
	var createdConnections int
	var attemptedConnections int
	for _, connectTo := range result.SuggestedConnections {
		// Skip duplicates.
		if connectTo.Duplicate {
			continue
		}

		// Check if connection already exists.
		crane := docks.GetAssignedCrane(connectTo.Hub.ID)
		if crane != nil {
			// Update last suggested timestamp.
			crane.NetState.UpdateLastSuggestedAt()
			// Continue crane if stopping.
			if crane.AbortStopping() {
				log.Infof("spn/captain: optimization aborted retiring of %s, removed stopping mark", crane)
				crane.NotifyUpdate()
			}

			// Create new connections if we have connects left.
		} else if createdConnections < result.MaxConnect {
			attemptedConnections++

			crane, tErr := EstablishPublicLane(ctx, connectTo.Hub)
			if !tErr.IsOK() {
				log.Warningf("spn/captain: failed to establish lane to %s: %s", connectTo.Hub, tErr)
			} else {
				createdConnections++
				crane.NetState.UpdateLastSuggestedAt()

				log.Infof("spn/captain: established lane to %s", connectTo.Hub)
			}
		}
	}

	// Log optimization result.
	if attemptedConnections > 0 {
		log.Infof(
			"spn/captain: created %d/%d new connections for %s optimization",
			createdConnections,
			attemptedConnections,
			result.Purpose)
	} else {
		log.Infof(
			"spn/captain: checked %d connections for %s optimization",
			len(result.SuggestedConnections),
			result.Purpose,
		)
	}

	// Retire cranes if unsuggested for a while.
	if result.StopOthers {
		for _, crane := range docks.GetAllAssignedCranes() {
			switch {
			case !crane.IsMine():
				// Skip cranes built by others.
			case crane.Stopped() || crane.IsStopping():
				// Skip cranes that are stopped or stopping.
			case crane.NetState.LastSuggestedAt().After(
				time.Now().Add(-stopCraneAfterBeingUnsuggestedFor),
			):
				// Skip cranes that were recently suggested.
			default:
				// Mark crane as stopping.
				if crane.MarkStopping() {
					log.Infof("spn/captain: retiring %s, marked as stopping", crane)
					crane.NotifyUpdate()
				}
			}
		}
	}

	return nil
}
