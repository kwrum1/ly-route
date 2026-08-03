package httpapi

import (
	"sort"
	"strings"
	"time"
)

const (
	gatewayLogicalEgressSampleLimit = 288
	gatewayLogicalEgressSeriesLimit = 1024
	gatewayConnectionRecordLimit    = 10_000
	gatewayTrafficSampleInterval    = 5 * time.Minute
)

func appendLogicalEgressSample(samples []LogicalEgressSample, counter LogicalEgressCounter, observedAt time.Time) []LogicalEgressSample {
	next := LogicalEgressSample{Timestamp: observedAt.UTC().Truncate(gatewayTrafficSampleInterval), Health: counter.Health, DownloadBytes: counter.DownloadBytes, UploadBytes: counter.UploadBytes}
	if len(samples) == 0 {
		return append(samples, next)
	}
	previous := samples[len(samples)-1]
	if next.Timestamp.Equal(previous.Timestamp) {
		if len(samples) > 1 {
			setLogicalEgressRate(&next, samples[len(samples)-2])
		}
		samples[len(samples)-1] = next
		return samples
	}
	setLogicalEgressRate(&next, previous)
	return boundedLogicalEgressSamples(append(samples, next))
}

func setLogicalEgressRate(next *LogicalEgressSample, previous LogicalEgressSample) {
	seconds := next.Timestamp.Sub(previous.Timestamp).Seconds()
	if seconds <= 0 || next.DownloadBytes < previous.DownloadBytes || next.UploadBytes < previous.UploadBytes {
		return
	}
	download := float64(next.DownloadBytes-previous.DownloadBytes) * 8 / seconds
	upload := float64(next.UploadBytes-previous.UploadBytes) * 8 / seconds
	next.DownloadBPS = &download
	next.UploadBPS = &upload
}

func retainedLogicalEgressSamples(samples []LogicalEgressSample, cutoff time.Time) []LogicalEgressSample {
	index := sort.Search(len(samples), func(index int) bool { return !samples[index].Timestamp.Before(cutoff) })
	return boundedLogicalEgressSamples(append([]LogicalEgressSample(nil), samples[index:]...))
}

func boundedLogicalEgressSamples(samples []LogicalEgressSample) []LogicalEgressSample {
	if len(samples) <= gatewayLogicalEgressSampleLimit {
		return samples
	}
	return append([]LogicalEgressSample(nil), samples[len(samples)-gatewayLogicalEgressSampleLimit:]...)
}

func retainedGatewayConnections(connections []GatewayConnection, cutoff time.Time) []GatewayConnection {
	result := make([]GatewayConnection, 0, len(connections))
	for _, connection := range connections {
		if !connection.ObservedAt.Before(cutoff) {
			result = append(result, connection)
		}
	}
	if len(result) > gatewayConnectionRecordLimit {
		return append([]GatewayConnection(nil), result[len(result)-gatewayConnectionRecordLimit:]...)
	}
	return result
}

func trimLogicalEgressHistories(series map[string]*logicalEgressHistory) {
	if len(series) <= gatewayLogicalEgressSeriesLimit {
		return
	}
	ids := make([]string, 0, len(series))
	for id := range series {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(left, right int) bool {
		leftSeen, rightSeen := series[ids[left]].lastSeen, series[ids[right]].lastSeen
		if leftSeen.Equal(rightSeen) {
			return ids[left] < ids[right]
		}
		return leftSeen.Before(rightSeen)
	})
	for _, id := range ids[:len(ids)-gatewayLogicalEgressSeriesLimit] {
		delete(series, id)
	}
}

func gatewayTrafficTrendWindow(window string) time.Duration {
	switch strings.TrimSpace(window) {
	case "realtime", "5m":
		return 5 * time.Minute
	case "1h":
		return time.Hour
	case "", "24h":
		return gatewayTelemetryRetention
	default:
		return gatewayTelemetryRetention
	}
}

func downsampleLogicalEgressSamples(samples []LogicalEgressSample, points int) []LogicalEgressSample {
	if points <= 0 || len(samples) <= points {
		return append([]LogicalEgressSample(nil), samples...)
	}
	step := float64(len(samples)) / float64(points)
	result := make([]LogicalEgressSample, 0, points)
	for index := 0; index < points; index++ {
		result = append(result, samples[int(float64(index)*step)])
	}
	return result
}
