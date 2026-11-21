package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/99apps-id/termixgo/internal/agent"
)

// Plan review
//
// submit_plan shows the finished plan in the transcript and asks, the way
// Claude Code's plan mode does, whether to start and how edits are confirmed,
// or what to change first.

type planReviewMsg struct {
	plan  string
	reply chan agent.PlanReview
}

var planReviewOptions = []struct {
	label   string
	mode    agent.ApprovalMode
	approve bool
}{
	{"Yes, and accept edits automatically", agent.ApprovalEdits, true},
	{"Yes, and confirm each change", agent.ApprovalAsk, true},
	{"No, keep planning (say what to change)", "", false},
}

// ReviewPlan implements app.PlanReviewer: it shows the plan and blocks the
// agent goroutine until the operator answers.
func (m *Model) ReviewPlan(plan string) (agent.PlanReview, error) {
	if m.program == nil {
		return agent.PlanReview{}, agent.ErrPlanReviewUnsupported
	}
	reply := make(chan agent.PlanReview, 1)
	m.program.Send(planReviewMsg{plan: plan, reply: reply})
	return awaitReply(reply, agent.PlanReview{}, approvalTimeout), nil
}

// openPlanReview puts the plan in the transcript and opens the dialog.
func (m *Model) openPlanReview(msg planReviewMsg) tea.Cmd {
	m.pendingPlan = &msg
	m.planCursor = 0
	m.planFeedback = false
	m.blocks = append(m.blocks, block{kind: blockAssistant, text: "**Plan for approval**\n\n" + strings.TrimSpace(msg.plan)})
	m.refresh()
	return tea.Batch(tea.SetWindowTitle(m.windowTitle()), bell())
}

// answerPlan sends the verdict and closes the dialog.
func (m *Model) answerPlan(review agent.PlanReview) (tea.Model, tea.Cmd) {
	if m.pendingPlan == nil {
		return m, nil
	}
	select {
	case m.pendingPlan.reply <- review:
	default:
	}
	text := "Plan sent back to keep planning."
	switch {
	case review.Approved && review.Mode == agent.ApprovalEdits:
		text = "Plan approved; edits are accepted automatically."
	case review.Approved:
		text = "Plan approved; each change asks first."
	case strings.TrimSpace(review.Feedback) != "":
		text = "Plan sent back: " + strings.TrimSpace(review.Feedback)
	}
	m.blocks = append(m.blocks, block{kind: blockNotice, text: text})
	m.pendingPlan = nil
	m.planFeedback = false
	m.input.SetValue("")
	m.input.Blur()
	m.refresh()
	return m, tea.SetWindowTitle(m.windowTitle())
}

func (m *Model) choosePlanOption(index int) (tea.Model, tea.Cmd) {
	option := planReviewOptions[index]
	if option.approve {
		return m.answerPlan(agent.PlanReview{Approved: true, Mode: option.mode})
	}
	m.planFeedback = true
	m.input.SetValue("")
	m.input.Placeholder = "What should change? Enter sends; empty just keeps planning"
	m.input.Focus()
	m.refresh()
	return m, nil
}

func (m *Model) handlePlanKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.planFeedback {
		switch key.String() {
		case "enter":
			return m.answerPlan(agent.PlanReview{Feedback: strings.TrimSpace(m.input.Value())})
		case "esc":
			m.planFeedback = false
			m.input.Blur()
			m.refresh()
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(key)
		m.refresh()
		return m, cmd
	}
	switch key.String() {
	case "up", "ctrl+p":
		m.planCursor = (m.planCursor - 1 + len(planReviewOptions)) % len(planReviewOptions)
	case "down", "ctrl+n", "tab":
		m.planCursor = (m.planCursor + 1) % len(planReviewOptions)
	case "enter":
		return m.choosePlanOption(m.planCursor)
	case "1", "2", "3":
		return m.choosePlanOption(int(key.String()[0] - '1'))
	case "esc":
		return m.answerPlan(agent.PlanReview{})
	}
	m.refresh()
	return m, nil
}

func (m *Model) viewPlanReview() string {
	width := m.dialogTextWidth()
	body := []string{
		m.styles.BoxTitle.Render("Ready to code?"),
		m.styles.Dim.Render(truncate("The plan is above. Approve it, or send it back with what to change.", width)),
		"",
	}
	for index, option := range planReviewOptions {
		row := fmt.Sprintf("%d. %s", index+1, option.label)
		if index == m.planCursor && !m.planFeedback {
			body = append(body, m.styles.MenuSelected.Render("> "+truncate(row, width-2)))
			continue
		}
		body = append(body, "  "+truncate(row, width-2))
	}
	if m.planFeedback {
		body = append(body, "", m.input.View(), m.styles.Hint.Render("Enter send | Esc back to the choices"))
	} else {
		body = append(body, "", m.styles.Hint.Render("Up/Down move | 1-3 choose | Enter select | Esc keep planning"))
	}
	return m.styles.Box.Width(m.width - 6).Render(strings.Join(body, "\n"))
}
