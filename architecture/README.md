# Architecture Documentation

This folder explains **agentman** for future AI agents and human contributors so you can
understand the system and plan changes *before* editing code — without re-discovering the
same context every time.

## How to use this folder

1. Read in the order below for a full mental model, or jump to the file that answers your question.
2. Treat **Confirmed** claims as facts, **Inference** as a hypothesis to double-check, and
   **Unknown** as a real gap (don't invent an answer).
3. Every non-obvious claim cites evidence (file paths / symbols). If you change something the
   evidence points at, update the doc.

## Recommended reading order

| # | File | Answers |
|---|------|---------|
| 1 | [project-overview.md](project-overview.md) | What is this and why does it exist? |
| 2 | [system-map.md](system-map.md) | How is it structured? Where does code live and run? |
| 3 | [backend.md](backend.md) | How does the server / API / data layer work? |
| 4 | [data-model.md](data-model.md) | What are the entities and relationships? |
| 5 | [frontend.md](frontend.md) | How does the dashboard work? |
| 6 | [security.md](security.md) | What is the trust model and what must I not break? |
| 7 | [engineering-conventions.md](engineering-conventions.md) | How do I write code that fits? |
| 8 | [decision-records.md](decision-records.md) | Why is it built this way? |
| 9 | [planning-guide.md](planning-guide.md) | How do I plan a change safely? |
| 10 | [contribution-guide.md](contribution-guide.md) | How do I set up, run, test, and add things? |
| 11 | [known-risks-and-gaps.md](known-risks-and-gaps.md) | What's uncertain, missing, or risky? |

## One-paragraph orientation

agentman is a **single Go binary** (`am`) that is both a localhost web server (`am serve`)
and a CLI. It is a tiny, self-hosted ticketing board **designed to be driven by AI agents**:
agents read/claim/update tasks via a terse CLI (or HTTP), and a human watches a live
SSE-powered dashboard. Storage is an embedded SQLite file. There is **no authentication** and
the server binds to `127.0.0.1` by design. See `README.md` (repo root) for the user-facing guide.

## ⚠️ Keep these docs in sync

These docs are only useful if they're true. When you change architecture, data model, routes,
the CLI surface, security posture, or build/release flow, **update the matching file in this
folder in the same change.** `planning-guide.md` → "Documentation Update Rules" lists what to
touch.

## Known documentation gaps

- No CI/CD exists, so there is no machine-enforced check that these docs stay accurate.
- Test coverage is narrow (see `known-risks-and-gaps.md`), so docs describe *intended* behavior
  more than *test-verified* behavior in several places — flagged inline where relevant.
- The frontend has no automated tests; its documented behavior is from source reading, not test evidence.
