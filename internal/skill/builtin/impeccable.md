---
name: impeccable
description: "Polish bar for refining an existing interface: typography, spacing, color, motion, copy, edge states."
---

# Impeccable

Use this skill when the operator asks to improve, polish, critique, audit, harden, animate, or adapt an existing frontend interface, or when a design reads as bland, loud, or unfinished. Covers websites, landing pages, dashboards, product UI, components, forms, settings, onboarding, and empty states. Not for backend-only or non-UI tasks.

## Process

1. Read the current implementation before judging it: layout tokens, type scale in use, spacing rhythm, color roles, motion, and copy tone.
2. Diagnose against the hierarchy: what competes, what disappears, where the eye lands first versus where it should. Rank findings by impact on the page goal.
3. Change the smallest thing that fixes each finding: tighten scale before restyling, align before recoloring, rewrite before rearranging.

## Rules

- Visual hierarchy: one dominant element per viewport, supporting elements step down in size and weight predictably. Never two elements shouting at once.
- Typography: fluid scale with a fixed ratio, line length 45 to 75 characters for prose, tabular numerals for data, no more than three sizes per view.
- Spacing: a single base unit multiplied consistently (4 or 8). Related items sit closer than unrelated ones; sections breathe more than components.
- Color: each hue has a job (action, danger, success, neutral). Interactive states differ in more than color alone.
- Micro-interaction: every action answers within 100ms with a visible state (hover, press, disabled, loading). Destructive actions confirm.
- UX copy: buttons name the outcome ("Publish changes", not "OK"), errors say what happened and what to do next, empty states offer one next step.
- Edge cases: long strings truncate gracefully, forms validate inline, failures keep input, offline or error states never strand the operator.
- Responsive and theme: the fix holds at 390px and at desktop width, in light and dark palettes, and with color disabled semantics preserved.
- Accessibility: focus order matches visual order, touch targets 44px minimum, motion respects prefers-reduced-motion.

## Output

Report findings as severity, location, issue, fix. Apply the agreed fixes in the existing code style, verify by rendering or testing where the stack allows, and leave the diff smaller than the diagnosis.
