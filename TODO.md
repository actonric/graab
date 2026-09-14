# To do

Open items, roughly in priority order. Tick them off in commits.

## Inbox

- [ ] **URGENT** Upload the new `new-app` skill to the Claude account
      - It exists locally at `~/.claude/skills/new-app` and in dotfiles (pushed
        2026-09-14), but cloud routines, Cowork and the desktop app load skills
        from the claude.ai account, and no browser was connected to do the upload.
      - Zip ready at `/private/tmp/claude-501/-Users-richard-code-spaan/3397c42f-8448-48ba-968e-b0aa16f6a878/scratchpad/skill-uploads/new-app.zip`
        (or re-zip: `cd ~/.claude/skills && zip -r new-app.zip new-app -x '*.DS_Store'`).
      - Steps: claude.ai → Customize → Skills → **Add** (it has never been
        uploaded) → pick the zip → confirm the page shows `new-app` under Yours.
      - Or run `/sync-skills` from a session with Claude in Chrome connected.

## Verify against the real account

- [x] **Linked-device name.** Verified on iOS: `-device-platform desktop`
      (the default) shows the bare name, "Local MCP Bridge". `unknown` shows
      "Other device" and hides the name. Keep `desktop` as the default.
- [x] Confirm history sync populates contact names from the address book
      (app-state sync) and that group senders resolve to names, not numbers.
      Verified: group participant names resolve.
- [ ] Send a text, a file from `store/outbox`, and a voice note. Exercised only
      against mocks so far. (Fly runs read-only until GRAAB_READ_ONLY is
      flipped.)
- [x] Download an incoming image. Verified 2026-09-12 on Fly v5: `download_media`
      returned a group flyer inline as an image block via the bridge's
      `/api/media` endpoint.

## Features

- [ ] Full-text search (SQLite FTS5) for `list_messages`; current `LIKE` scan
      is fine to ~50k messages.
- [ ] Reactions and read receipts (currently dropped).
- [x] OAuth 2.1 for the HTTP transport so claude.ai connectors and cloud
      Claude Code can use a public deployment (`GRAAB_MCP_PUBLIC_URL`).
- [x] Confirm the claude.ai custom-connector flow end to end against the Fly
      deployment (dynamic registration + login page). Verified 2026-09-07,
      including secret rotation invalidating the old session.

## Housekeeping

- [ ] Switch the GitHub default branch to `main` and delete
      `claude/hn-item-43532967-sc82js` (GitHub refuses to delete the default).
- [ ] Bump whatsmeow periodically; CI catches API breakage.
