package skill

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// maxProposalBytes bounds a proposed skill body, so a runaway generation cannot
// fill the state directory.
const maxProposalBytes = 40000

// Proposal actions.
const (
	ProposalCreate = "create"
	ProposalUpdate = "update"
)

// ProposalStatusStale marks a proposal whose target changed after the proposal
// was written, so applying it would clobber newer work.
const ProposalStatusStale = "stale"

// Proposal is a staged skill change. Generated content never lands in the live
// skills folder on its own: the operator applies or rejects it, and an update
// is refused when the target has moved on since the proposal was made.
type Proposal struct {
	ID          string    `json:"id"`
	Action      string    `json:"action"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Summary     string    `json:"summary,omitempty"`
	TargetHash  string    `json:"targetHash,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	Status      string    `json:"status"`
	Body        string    `json:"body"`
}

// ProposalsDir is the proposal queue inside the workspace state directory.
func ProposalsDir(workspace string) string {
	return filepath.Join(workspace, ".termixgo", "skill-proposals")
}

// Propose stages a create or update proposal. The action is inferred: an
// existing project skill becomes an update, anything else a create. An update
// records the target's hash so apply can detect that it has changed.
func Propose(workspace, name, description, body, summary string) (Proposal, error) {
	if strings.TrimSpace(workspace) == "" {
		return Proposal{}, errors.New("a workspace is required to propose a skill")
	}
	normalised := NormalizeName(name)
	if normalised == "" {
		return Proposal{}, errors.New("skill name must contain a letter or digit")
	}
	trimmedBody := strings.TrimSpace(body)
	if trimmedBody == "" {
		return Proposal{}, errors.New("the proposed skill body is empty; write the SKILL.md content first")
	}
	if len(trimmedBody) > maxProposalBytes {
		return Proposal{}, fmt.Errorf("the proposed skill is %d bytes, over the %d limit", len(trimmedBody), maxProposalBytes)
	}
	if strings.ContainsRune(trimmedBody, '\x00') {
		return Proposal{}, errors.New("the proposed skill contains a null byte")
	}
	if strings.TrimSpace(description) == "" {
		description = "A Termixgo skill."
	}

	action := ProposalCreate
	targetHash := ""
	document := filepath.Join(ProjectDir(workspace), normalised, "SKILL.md")
	if data, err := os.ReadFile(document); err == nil {
		action = ProposalUpdate
		targetHash = documentHash(data)
	}

	id, err := proposalID()
	if err != nil {
		return Proposal{}, err
	}
	proposal := Proposal{
		ID:          id,
		Action:      action,
		Name:        normalised,
		Description: strings.TrimSpace(description),
		Summary:     strings.TrimSpace(summary),
		TargetHash:  targetHash,
		CreatedAt:   time.Now(),
		Status:      "pending",
		Body:        trimmedBody,
	}
	if err := writeProposal(workspace, proposal); err != nil {
		return Proposal{}, err
	}
	return proposal, nil
}

// ListProposals returns the queue, newest first.
func ListProposals(workspace string) ([]Proposal, error) {
	entries, err := os.ReadDir(ProposalsDir(workspace))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read proposals: %w", err)
	}
	proposals := make([]Proposal, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		if proposal, err := readProposalFile(filepath.Join(ProposalsDir(workspace), entry.Name())); err == nil {
			proposals = append(proposals, proposal)
		}
	}
	sort.SliceStable(proposals, func(i, j int) bool {
		return proposals[i].CreatedAt.After(proposals[j].CreatedAt)
	})
	return proposals, nil
}

// GetProposal returns one proposal by id.
func GetProposal(workspace, id string) (Proposal, error) {
	return readProposalFile(filepath.Join(ProposalsDir(workspace), strings.TrimSpace(id)+".json"))
}

// ApplyProposal writes a proposal to the live skills folder and removes it from
// the queue. An update whose target changed since the proposal was made is
// marked stale and refused, so a proposal cannot silently overwrite newer work.
func ApplyProposal(workspace, id string) (Skill, error) {
	proposal, err := GetProposal(workspace, id)
	if err != nil {
		return Skill{}, err
	}
	document := filepath.Join(ProjectDir(workspace), proposal.Name, "SKILL.md")
	if proposal.Action == ProposalUpdate {
		data, err := os.ReadFile(document)
		if err != nil {
			return Skill{}, fmt.Errorf("the target skill is gone: %w", err)
		}
		if documentHash(data) != proposal.TargetHash {
			proposal.Status = ProposalStatusStale
			_ = writeProposal(workspace, proposal)
			return Skill{}, errors.New("the target skill changed since this proposal was made; review it again")
		}
	}
	if err := os.MkdirAll(filepath.Dir(document), 0o755); err != nil {
		return Skill{}, fmt.Errorf("create skill folder: %w", err)
	}
	content := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n%s\n", proposal.Name, proposal.Description, proposal.Body)
	if err := os.WriteFile(document, []byte(content), 0o644); err != nil {
		return Skill{}, fmt.Errorf("write SKILL.md: %w", err)
	}
	if err := RejectProposal(workspace, proposal.ID); err != nil {
		return Skill{}, err
	}
	return Load(workspace, proposal.Name)
}

// RejectProposal drops a proposal without applying it.
func RejectProposal(workspace, id string) error {
	path := filepath.Join(ProposalsDir(workspace), strings.TrimSpace(id)+".json")
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func writeProposal(workspace string, proposal Proposal) error {
	dir := ProposalsDir(workspace)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create proposals directory: %w", err)
	}
	data, err := json.MarshalIndent(proposal, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, proposal.ID+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readProposalFile(path string) (Proposal, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Proposal{}, fmt.Errorf("read proposal: %w", err)
	}
	var proposal Proposal
	if err := json.Unmarshal(data, &proposal); err != nil {
		return Proposal{}, fmt.Errorf("parse proposal: %w", err)
	}
	return proposal, nil
}

func documentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func proposalID() (string, error) {
	buffer := make([]byte, 4)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate a proposal id: %w", err)
	}
	return hex.EncodeToString(buffer), nil
}
