# graab — TODO

## Inbox

- [ ] **#4** Full-text search (SQLite FTS5) for `list_messages`
      - The current `LIKE` scan is fine to ~50k messages.

- [ ] **#5** Reactions and read receipts
      - Currently dropped by the bridge.

- [ ] **#6** Bump whatsmeow periodically
      - CI catches API breakage.

## In Progress

## Done

- [x] **#2** **Done 2026-09-16.** Send a text, a file from `store/outbox`, and a voice note
      against the real account
      - All to Richard's own number on Fly v7: text, an image from `/data/media`
        (downloaded first), a WAV document from `/data/outbox`, and the same WAV
        as a voice note (ffmpeg converted it to Ogg Opus). A send to a number
        outside GRAAB_ALLOWED_RECIPIENTS was refused with the expected message.
      - `/data/outbox/graab-test-tone.wav` (2 s, 440 Hz, made with ffmpeg over
        `fly ssh console`) is still there; delete it or keep it as a test fixture.

- [x] **#3** **Done 2026-09-16.** Exercise polls and events against the real account
      - Poll created, vote cast, event created, RSVP "going" with one extra guest;
        `list_messages include_details` on the own chat showed the vote and the
        RSVP decrypted and counted. The hand-built event and RSVP protobufs work.
      - Not yet checked: polls and events arriving from other people's group
        history (no recent ones existed to check against).

- [x] **#15** **Done 2026-09-16.** Remove and re-add the WhatsApp connector in claude.ai
      - After the re-add the connector exposed `send_poll` and the other sending
        tools; the first real poll went to Richard's own chat at 06:08 UTC.

- [x] **#1** **Done 2026-09-16.** Finish the GitHub → Fly deploy setup
      - FLY_API_TOKEN secret set, `.github/workflows/deploy.yml` deployed v7 on
        the first push to main. Bridge log confirms allowed recipients
        [18134953896] and read-only off; bridge_status agrees.

- [x] **#7** **Done 2026-09-14.** Switch the GitHub default branch to `main` and delete
      `claude/hn-item-43532967-sc82js`
      - Default is `main`; the branch was deleted with `git push origin --delete`
        on 2026-09-14.

- [x] **#8** **Done 2026-09-14.** Upload the new `new-app` skill to the Claude account
      - It exists locally at `~/.claude/skills/new-app` and in dotfiles (pushed
        2026-09-14), but cloud routines, Cowork and the desktop app load skills
        from the claude.ai account, and no browser was connected to do the upload.
      - Zip ready at `/private/tmp/claude-501/-Users-richard-code-spaan/3397c42f-8448-48ba-968e-b0aa16f6a878/scratchpad/skill-uploads/new-app.zip`
        (or re-zip: `cd ~/.claude/skills && zip -r new-app.zip new-app -x '*.DS_Store'`).
      - Steps: claude.ai → Customize → Skills → **Add** (it has never been
        uploaded) → pick the zip → confirm the page shows `new-app` under Yours.
      - Or run `/sync-skills` from a session with Claude in Chrome connected.

- [x] **#9** **Done 2026-09-14.** Polls and events: options, votes, dates, locations and RSVPs,
      plus creating and answering them

- [x] **#10** **Done 2026-09-12.** Download an incoming image
      - Verified on Fly v5: `download_media` returned a group flyer inline as an
        image block via the bridge's `/api/media` endpoint.

- [x] **#11** **Done 2026-09-07.** Confirm the claude.ai custom-connector flow end to end against
      the Fly deployment (dynamic registration + login page)
      - Verified including secret rotation invalidating the old session.

- [x] **#12** **Done 2026-09-07.** OAuth 2.1 for the HTTP transport so claude.ai connectors and
      cloud Claude Code can use a public deployment (`GRAAB_MCP_PUBLIC_URL`)

- [x] **#13** **Done 2026-09-07.** Linked-device name
      - Verified on iOS: `-device-platform desktop` (the default) shows the bare
        name, "Local MCP Bridge". `unknown` shows "Other device" and hides the
        name. Keep `desktop` as the default.

- [x] **#14** **Done 2026-09-07.** Confirm history sync populates contact names from the address
      book (app-state sync) and that group senders resolve to names, not numbers
      - Verified: group participant names resolve.

## Someday/Maybe
