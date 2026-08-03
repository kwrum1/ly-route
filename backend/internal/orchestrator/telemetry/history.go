package telemetry

import "time"

func (collector *Collector) record(timestamp time.Time, totals BoundaryTotals, groups []GroupTraffic, connections []TopConnection) {
	if totals.WAN.State != StateUnavailable && totals.LAN.State != StateUnavailable && newerTrafficPoint(collector.trafficHistory, timestamp) {
		collector.trafficHistory = append(collector.trafficHistory, TrafficPoint{
			Timestamp: timestamp,
			Totals:    totals,
			Groups:    append([]GroupTraffic(nil), groups...),
		})
		collector.trafficHistory = boundTrafficHistory(collector.trafficHistory)
	}
	if newerConnectionPoint(collector.connectionHistory, timestamp) {
		collector.connectionHistory = append(collector.connectionHistory, ConnectionPoint{
			Timestamp:   timestamp,
			Connections: cloneConnections(connections),
		})
		collector.connectionHistory = boundConnectionHistory(collector.connectionHistory)
	}
}

func boundTrafficHistory(points []TrafficPoint) []TrafficPoint {
	if len(points) <= maxHistoryPoints {
		return points
	}
	return points[len(points)-maxHistoryPoints:]
}

func boundConnectionHistory(points []ConnectionPoint) []ConnectionPoint {
	if len(points) <= maxHistoryPoints {
		return points
	}
	return points[len(points)-maxHistoryPoints:]
}

func (collector *Collector) prune(now time.Time) {
	cutoff := now.Add(-retentionWindow)
	trafficIndex := 0
	for trafficIndex < len(collector.trafficHistory) && collector.trafficHistory[trafficIndex].Timestamp.Before(cutoff) {
		trafficIndex++
	}
	collector.trafficHistory = append([]TrafficPoint(nil), collector.trafficHistory[trafficIndex:]...)
	connectionIndex := 0
	for connectionIndex < len(collector.connectionHistory) && collector.connectionHistory[connectionIndex].Timestamp.Before(cutoff) {
		connectionIndex++
	}
	collector.connectionHistory = append([]ConnectionPoint(nil), collector.connectionHistory[connectionIndex:]...)
}

func (collector *Collector) history() History {
	traffic := make([]TrafficPoint, len(collector.trafficHistory))
	for index, point := range collector.trafficHistory {
		traffic[index] = point
		traffic[index].Groups = append([]GroupTraffic(nil), point.Groups...)
	}
	connections := make([]ConnectionPoint, len(collector.connectionHistory))
	for index, point := range collector.connectionHistory {
		connections[index] = ConnectionPoint{Timestamp: point.Timestamp, Connections: cloneConnections(point.Connections)}
	}
	return History{WindowSeconds: int64(retentionWindow / time.Second), Traffic: traffic, Connections: connections}
}

func newerTrafficPoint(points []TrafficPoint, timestamp time.Time) bool {
	return len(points) == 0 || timestamp.After(points[len(points)-1].Timestamp)
}

func newerConnectionPoint(points []ConnectionPoint, timestamp time.Time) bool {
	return len(points) == 0 || timestamp.After(points[len(points)-1].Timestamp)
}

func cloneConnections(items []TopConnection) []TopConnection {
	clone := make([]TopConnection, len(items))
	for index, item := range items {
		clone[index] = item
		clone[index].Groups = append([]string(nil), item.Groups...)
	}
	return clone
}
