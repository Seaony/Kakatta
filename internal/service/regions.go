package service

import (
	"context"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/regions"
)

func (s *Service) ListRegions(ctx context.Context, parentID string) ([]RegionNode, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	nodes, err := regions.List(parentID)
	if err != nil {
		return nil, queryError()
	}
	out := make([]RegionNode, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, RegionNode(node))
	}
	return out, nil
}

func regionCities() []analysis.RegionCity {
	cities := regions.Cities()
	out := make([]analysis.RegionCity, 0, len(cities))
	for _, city := range cities {
		out = append(out, analysis.RegionCity{Province: city.Province, City: city.City})
	}
	return out
}
