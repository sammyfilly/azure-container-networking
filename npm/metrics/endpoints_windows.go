package metrics

import "github.com/Azure/azure-container-networking/npm/util"

func RecordListEndpointsLatency(timer *Timer) {
	if util.IsWindowsDP() {
		listEndpointsLatency.Observe(timer.timeElapsed())
	}
}

func IncListEndpointsFailures() {
	if util.IsWindowsDP() {
		listEndpointsFailures.Inc()
	}
}

func RecordGetEndpointLatency(timer *Timer) {
	if util.IsWindowsDP() {
		getEndpointLatency.Observe(timer.timeElapsed())
	}
}

func IncGetEndpointFailures() {
	if util.IsWindowsDP() {
		getEndpointFailures.Inc()
	}
}

// TotalListEndpointsLatencyCalls returns the number of times RecordListEndpointsLatency has been called.
// This function is slow.
func TotalListEndpointsLatencyCalls() (int, error) {
	return histogramCount(listEndpointsLatency)
}

// TotalListEndpointsFailures returns the number of times IncListEndpointsFailures has been called.
func TotalListEndpointsFailures() (int, error) {
	return counterValue(listEndpointsFailures)
}

// TotalGetEndpointLatencyCalls returns the number of times RecordGetEndpointLatency has been called.
// This function is slow.
func TotalGetEndpointLatencyCalls() (int, error) {
	return histogramCount(getEndpointLatency)
}

// TotalGetEndpointFailures returns the number of times IncGetEndpointFailures has been called.
// This function is slow.
func TotalGetEndpointFailures() (int, error) {
	return counterValue(getEndpointFailures)
}
