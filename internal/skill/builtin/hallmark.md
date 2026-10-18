---
name: hallmark
description: "Design bar for greenfield pages and redesigns. Study before proposing any UI/UX direction."
---

# Hallmark

Use this skill when the operator asks to build, restyle, redesign, audit, or study the design of a website, landing page, dashboard, app screen, or any visual interface. Also use it when extracting a design direction from a URL or screenshot. Not for backend-only or non-UI tasks.

## Process

1. Understand the product first: who it serves, the single action the page must drive, and the brand voice (quiet luxury, bold consumer, clinical enterprise, playful).
2. Study references before proposing: named sites, screenshots under discussion, or a short competitive scan. Name what each reference teaches (rhythm, type scale, color discipline), then commit to one direction. Never blend two references into a compromise.
3. Set the aesthetic thesis in one sentence, for example "dark editorial with oversized serif and surgical whitespace", and judge every later choice against it.

## Rules

- One voice: pick two typefaces at most (one display, one text), two to three hues plus neutrals, one corner radius family, one shadow family.
- Hierarchy over decoration: a page gets one hero idea, one primary action, and visual weight that falls in reading order. Remove before embellishing.
- Real content, not lorem: write concrete copy sized to the layout, with a state for empty, loading, and error wherever data appears.
- Responsive truth: state how the hero, the nav, and any multi-column block collapse at 390px. A design that only works at desktop width is unfinished.
- Motion with a job: at most one entrance motif plus hover feedback. No autoplay video without mute and pause, no parallax that moves text.
- Accessibility floor: contrast 4.5:1 for body text, visible focus rings, semantic landmarks, alt text on meaningful images, keyboard path for every action.
- Performance posture: no layout shift on load, images sized and lazy below the fold, no dependency added for what CSS already does.

## Output

Present the direction as thesis, then palette, type scale, and layout beats. Deliver code or markup that compiles and respects the repo conventions it lands in. Note what you deliberately left out and why.
