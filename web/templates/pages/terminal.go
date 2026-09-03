package pages

import (
	"strings"

	"gatstogo/internal/domain"
)

func terminalPlantName(plant *domain.Plant) string {
	if plant == nil || strings.TrimSpace(plant.Name) == "" {
		return "Gas plant"
	}
	return strings.TrimSpace(plant.Name)
}
