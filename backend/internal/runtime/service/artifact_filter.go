package service

const ReloadModePersistOnly = "persist-only"

func WithoutServices(artifacts []RenderedArtifact, excluded ...ServiceName) []RenderedArtifact {
	if len(excluded) == 0 {
		return artifacts
	}
	blocked := make(map[ServiceName]struct{}, len(excluded))
	for _, service := range excluded {
		blocked[service] = struct{}{}
	}
	filtered := make([]RenderedArtifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if _, ok := blocked[artifact.Service]; ok {
			continue
		}
		filtered = append(filtered, artifact)
	}
	return filtered
}

func PersistOnlyForServices(artifacts []RenderedArtifact, services ...ServiceName) []RenderedArtifact {
	if len(services) == 0 {
		return artifacts
	}
	targets := make(map[ServiceName]struct{}, len(services))
	for _, service := range services {
		targets[service] = struct{}{}
	}
	result := append([]RenderedArtifact(nil), artifacts...)
	for index := range result {
		if _, ok := targets[result[index].Service]; ok {
			result[index].ReloadMode = ReloadModePersistOnly
		}
	}
	return result
}

func artifactsArePersistOnly(artifacts []RenderedArtifact) bool {
	if len(artifacts) == 0 {
		return false
	}
	for _, artifact := range artifacts {
		if artifact.ReloadMode != ReloadModePersistOnly {
			return false
		}
	}
	return true
}
