package manager

import "github.com/stashapp/stash/pkg/generationbudget"

func (g *SpriteGenerator) spriteBudgetEnabled() bool {
	if g.g.Budget != nil {
		return true
	}
	if config, ok := g.g.FFMpegConfig.(interface {
		GetGenerationBudget() *generationbudget.Budget
	}); ok {
		return config.GetGenerationBudget() != nil
	}
	return false
}
