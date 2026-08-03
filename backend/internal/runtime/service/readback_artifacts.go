package service

import (
	"fmt"
	"strings"
)

func artifactContent(artifacts []RenderedArtifact, path string) (string, error) {
	for _, artifact := range artifacts {
		if artifact.Path == path {
			if strings.TrimSpace(artifact.Content) == "" {
				return "", fmt.Errorf("readback artifact %s is empty", path)
			}
			return artifact.Content, nil
		}
	}
	return "", fmt.Errorf("readback artifact %s is missing", path)
}

func normalizedLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
