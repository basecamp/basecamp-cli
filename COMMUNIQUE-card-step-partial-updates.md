# Communique: Presence-Aware Partial Updates for Card Steps

**From:** Basecamp CLI team
**To:** BC3 Rails development
**Re:** `steps#update` clears `due_on` and assignees when they are omitted
**Context:** CLI issue #604 — live data loss on released v0.8.0 / v0.8.1

---

## The problem

A single-field update to a card step destroys the fields it did not mention.
Reproduced against production on released CLI v0.8.0:

```
$ basecamp cards step create "Ship the thing" --card <card> --in <project> --due 2026-08-14
# step: title set, due_on 2026-08-14

$ basecamp assign <step> --step --to me --in <project>
# ok: true — assignee set, and due_on is now null

$ basecamp cards step update <step> --in <project> --due 2026-08-14
# ok: true — due_on restored, and assignees are now []
```

There is no client-side spelling of these commands that avoids it, which is
what makes this a contract question rather than a CLI bug.

## Why the client cannot fix it

`app/controllers/steps_controller.rb`:

```ruby
def update
  @recording.update! recordable: new_step
  @recording.replace_assignees find_assignees, notify: true

  render :show
end

private
  def new_step
    Step.new step_params
  end

  def step_params
    normalized_params.expect(step: [ :title, :due_on ]).tap do |p|
      p[:title]&.strip!
    end
  end

  def assignee_ids
    normalized_params.dig(:step, :assignee_ids) || normalized_params.dig(:step, :assignees).to_s.split(",")
  end
```

Two independent clears, both unconditional:

1. **`Step.new step_params`** constructs a *fresh* `Step` from whatever the
   request carried and swaps it in as the recordable. An omitted `due_on`
   yields a `Step` that has none. This is also why the CLI already has to
   re-send `title` on every update — omitting it produces a titleless `Step`,
   which fails validation with a 400 (CLI #496).
2. **`replace_assignees find_assignees`** runs on every request, not only when
   the request mentioned assignees. With `assignee_ids` absent, `assignee_ids`
   falls through to `"".split(",")` → `[]`, so `find_assignees` resolves to an
   empty relation and every assignee is removed.

Both clears happen server-side, from the *absence* of a key. No client change
reaches them. Omitting a field and explicitly clearing it are the same request
on the wire today, so a client cannot express "leave this alone" at all —
including a client that made every field nullable specifically to draw that
distinction.

The only client-side workaround is read-modify-write: `GET` the step and echo
back every field on every mutation. That is racy (it clobbers a concurrent
edit between the read and the write), it doubles the request count for every
step mutation, and each API consumer has to reimplement it. We would rather
not, and #604's original framing — that the CLI owns preservation — was wrong.

## What `cards#update` already does

`app/controllers/kanban/cards_controller.rb` has half of the answer already:

```ruby
def update
  @recording.update! recordable: @recording.recordable.changing(card_update_params)
  @recording.replace_assignees(find_assignees, notify: notify_assignees_param) if update_assignees?
end

private
  def update_assignees?
    params[:kanban_card].has_key?(:assignee_ids) || params[:kanban_card].has_key?(:assignees)
  end

  def card_update_params
    # sets due_on to nil if it's not included in the params
    { due_on: nil }.merge(card_params)
  end
```

Two things differ from steps:

- **Assignees are presence-aware.** `update_assignees?` tests `has_key?`, so an
  omitted key leaves assignees untouched while an explicit `assignee_ids: []`
  still clears them. This is exactly the behaviour steps need, and the
  mechanism already exists in the codebase.
- **The recordable is mutated, not replaced.** `recordable.changing(...)`
  preserves attributes the request did not mention, so `title` and `content`
  round-trip on cards. Steps construct a new `Step`, so nothing round-trips.

`due_on` is the exception: cards nil it deliberately when omitted, per the
comment on `card_update_params`. So cards have the same `due_on` behaviour that
#604 reports for steps — it is intentional there, presumably because the card
form always submits the field. We are not assuming that intent extends to a
JSON API client, and we would rather ask than guess.

## What we are asking for

**Make `steps#update` presence-aware, on both fields.**

```ruby
def update
  @recording.update! recordable: @recording.recordable.changing(step_params)
  @recording.replace_assignees(find_assignees, notify: true) if update_assignees?

  render :show
end

private
  def update_assignees?
    normalized_params[:step].has_key?(:assignee_ids) || normalized_params[:step].has_key?(:assignees)
  end
```

Resulting contract, for a `PUT`/`PATCH` to a step:

| Request contains | `due_on` | assignees |
|---|---|---|
| neither key | unchanged | unchanged |
| `due_on: "2026-08-14"` | set | unchanged |
| `due_on: null` | cleared | unchanged |
| `assignee_ids: [1,2]` | unchanged | set to 1,2 |
| `assignee_ids: []` | unchanged | cleared |

Omission means "leave alone"; an explicit `null` / `[]` means "clear". That is
the distinction the wire format can carry and the current controller cannot.

If `changing` on the recordable is the wrong shape for `Step`, merging the
permitted params over the current recordable's attributes gets to the same
place — the requirement is only that an absent key does not participate.

### Tests we would ask for

Three cases, because two of them are the reported bug and the third is the one
a naive fix breaks:

1. **Due-only preservation** — `PUT` with `assignee_ids` and no `due_on`; the
   existing `due_on` survives.
2. **Assignee-only preservation** — `PUT` with `due_on` and no assignee key;
   the existing assignees survive.
3. **Explicit clear still clears** — `PUT` with `due_on: null` clears the due
   date; `PUT` with `assignee_ids: []` removes every assignee. A fix that
   guards on blankness rather than key presence passes 1 and 2 and silently
   breaks this one.

### Would `title` come along?

`changing` would also make `title` optional on update, which would let the CLI
drop the extra `GET` that #496 added purely to re-send an unchanged title. Not
required, and not what this is about — but it falls out of the same change, and
we would happily delete that workaround.

## Why this is worth doing server-side

A deployed BC3 fix stops the data loss for every already-installed v0.8.x
client, with no CLI release, no SDK release, and nothing for users to upgrade.
The reverse is not true: no client release can fix it at all.

## Impact on existing behavior

For any client that sends the full object on every update — which is what the
web UI's form does — nothing changes. What changes is that a client which
*omits* a field stops having it cleared. Any caller relying on omission-clears
would be affected; we do not know of one, and it is not a documented behaviour
in [bc3-api](https://github.com/basecamp/bc3-api).

## Follow-on, on our side

Once the server honours the distinction, we would pointerize `due_on` on the
SDK's `UpdateStepRequest` — mirroring `UpdateCardRequest`, which was
pointerized in SDK v0.10.0 — so a client can *express* omit-versus-clear. That
change is inert until the server-side fix lands: a nil `*string` is omitted
from the JSON either way, and today's controller clears on that omission
regardless.

---

**Question for BC3:** Is presence-awareness the shape you want here, or is the
wholesale replace in `steps#update` load-bearing for something we cannot see
from outside? And is `card_update_params`' deliberate `due_on: nil` still
intended for JSON API callers, or is that form-shaped behaviour that cards
would also want revisited?
