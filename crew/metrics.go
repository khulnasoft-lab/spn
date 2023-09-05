package crew

import (
	"sync/atomic"

	"github.com/tevino/abool"

	"github.com/safing/portbase/api"
	"github.com/safing/portbase/metrics"
)

var (
	newConnectOp           *metrics.Counter
	connectOpIncomingBytes *metrics.Counter
	connectOpOutgoingBytes *metrics.Counter

	connectOpTTCRDurationHistogram *metrics.Histogram
	connectOpTTFBDurationHistogram *metrics.Histogram
	connectOpDurationHistogram     *metrics.Histogram
	connectOpIncomingDataHistogram *metrics.Histogram
	connectOpOutgoingDataHistogram *metrics.Histogram

	metricsRegistered = abool.New()
)

func registerMetrics() (err error) {
	// Only register metrics once.
	if !metricsRegistered.SetToIf(false, true) {
		return nil
	}

	// Connect Op Stats.

	newConnectOp, err = metrics.NewCounter(
		"spn/op/connect/total",
		nil,
		&metrics.Options{
			Name:       "SPN Total Connect Operations",
			InternalID: "spn_connect_count",
			Permission: api.PermitUser,
			Persist:    true,
		},
	)
	if err != nil {
		return err
	}

	_, err = metrics.NewGauge(
		"spn/op/connect/active",
		nil,
		getActiveConnectOpsStat,
		&metrics.Options{
			Name:       "SPN Active Connect Operations",
			Permission: api.PermitUser,
		},
	)
	if err != nil {
		return err
	}

	connectOpIncomingBytes, err = metrics.NewCounter(
		"spn/op/connect/incoming/bytes",
		nil,
		&metrics.Options{
			Name:       "SPN Connect Operation Incoming Bytes",
			InternalID: "spn_connect_in_bytes",
			Permission: api.PermitUser,
			Persist:    true,
		},
	)
	if err != nil {
		return err
	}

	connectOpOutgoingBytes, err = metrics.NewCounter(
		"spn/op/connect/outgoing/bytes",
		nil,
		&metrics.Options{
			Name:       "SPN Connect Operation Outgoing Bytes",
			InternalID: "spn_connect_out_bytes",
			Permission: api.PermitUser,
			Persist:    true,
		},
	)
	if err != nil {
		return err
	}

	connectOpTTCRDurationHistogram, err = metrics.NewHistogram(
		"spn/op/connect/histogram/ttcr/seconds",
		nil,
		&metrics.Options{
			Name:       "SPN Connect Operation time-to-connect-request Histogram",
			Permission: api.PermitUser,
		},
	)
	if err != nil {
		return err
	}

	connectOpTTFBDurationHistogram, err = metrics.NewHistogram(
		"spn/op/connect/histogram/ttfb/seconds",
		nil,
		&metrics.Options{
			Name:       "SPN Connect Operation time-to-first-byte (from TTCR) Histogram",
			Permission: api.PermitUser,
		},
	)
	if err != nil {
		return err
	}

	connectOpDurationHistogram, err = metrics.NewHistogram(
		"spn/op/connect/histogram/duration/seconds",
		nil,
		&metrics.Options{
			Name:       "SPN Connect Operation Duration Histogram",
			Permission: api.PermitUser,
		},
	)
	if err != nil {
		return err
	}

	connectOpIncomingDataHistogram, err = metrics.NewHistogram(
		"spn/op/connect/histogram/incoming/bytes",
		nil,
		&metrics.Options{
			Name:       "SPN Connect Operation Downloaded Data Histogram",
			Permission: api.PermitUser,
		},
	)
	if err != nil {
		return err
	}

	connectOpOutgoingDataHistogram, err = metrics.NewHistogram(
		"spn/op/connect/histogram/outgoing/bytes",
		nil,
		&metrics.Options{
			Name:       "SPN Connect Operation Outgoing Data Histogram",
			Permission: api.PermitUser,
		},
	)
	if err != nil {
		return err
	}

	return nil
}

func getActiveConnectOpsStat() float64 {
	return float64(atomic.LoadInt64(activeConnectOps))
}
