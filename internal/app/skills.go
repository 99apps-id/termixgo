package app

import "github.com/99apps-id/termixgo/internal/skill"

// SkillProposals lists the staged skill changes awaiting review.
func (a *App) SkillProposals() ([]skill.Proposal, error) {
	return skill.ListProposals(a.workspace)
}

// ApplySkillProposal writes a proposal to the live skills folder and reloads
// the catalogue, so the change is visible to the next turn.
func (a *App) ApplySkillProposal(id string) (skill.Skill, error) {
	applied, err := skill.ApplyProposal(a.workspace, id)
	if err != nil {
		return skill.Skill{}, err
	}
	a.ReloadSkills()
	return applied, nil
}

// RejectSkillProposal drops a proposal without applying it.
func (a *App) RejectSkillProposal(id string) error {
	return skill.RejectProposal(a.workspace, id)
}
