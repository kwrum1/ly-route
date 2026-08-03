package service

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
