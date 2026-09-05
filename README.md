# XP_Gain

A fitness RPG: log meals by photo, and your character's base stats move with your
nutrition adherence.

- `server/` — Go service. Vision AI intake, macro/stat transaction, SQLite, Firebase sync staging.
- `app/` — Flutter client. Layered pixel-art avatar renderer.
- `art/` — source art (Aseprite).

> The previous .NET MAUI + MonoGame implementation was removed in favour of this
> stack. It remains in git history at `80d1e4d` and on `origin/main`.

---

## 1. Macro intake and the fallback workflow

### The routing decision

Everything hinges on one flag in the parsed Vision AI payload:

```
POST /v1/intake/photo
        │
        ▼
  vision.Payload.Accepted()
        │
   ┌────┴─────┐
   │          │
 Path A     Path B
 accepted   rejected
   │          │
   │          └─► persist intake_rejections row
   │              422 + validation_reasoning + rejection_id
   │                    │
   │            ┌───────┴────────┐
   │            ▼                ▼
   │     POST /intake/photo   POST /intake/manual
   │     (reattempt)          (is_manual = true)
   │            │                │
   └────────────┴────────────────┘
                ▼
        service.commit() — one transaction
```

Three conditions send a payload down Path B, collapsed into one branch so the
service layer has a single decision to make:

1. `is_valid_food` is false;
2. the model claimed food below the confidence floor (`vision.MinConfidence`, 0.55);
3. the model claimed food but returned macros outside a plausible range.

A model asserting "this is food" at 20% confidence is guessing, and a guess that
silently moves a player's base stats is worse than an honest refusal the player
can correct.

### Why rejections are persisted

Path B writes an `intake_rejections` row before returning. That row is what makes
the fallback resumable: the 422 carries a `rejection_id`, and the client echoes it
back as `supersedes_rejection_id` on either the manual override or the photo
reattempt. The resulting entry is permanently linked to the refusal it answered,
so the pedigree trail can show that a user typed values after the AI declined.

### Data pedigree

| `source`    | `is_manual` | Meaning                                 |
|-------------|-------------|-----------------------------------------|
| `photo`     | 0           | AI-validated, first attempt             |
| `reattempt` | 0           | AI-validated, after an earlier rejection |
| `manual`    | 1           | User-typed, never seen by a model       |

`is_manual` is derived from the route, never read from the request body — a
client cannot submit typed numbers labelled as AI-verified. Manual entries earn
half XP, but full CON/VIT: the food was still eaten, only the evidence is weaker.

### The transaction

`service.commit` is the single write path for an accepted entry, photo or manual
alike. One transaction covers:

1. the `macro_entries` row,
2. the day's running totals in `daily_macro_totals`,
3. the character's `con_micro` / `vit_micro` / `xp` / `level`,
4. the streak counters and `last_activity_at`,
5. `is_synced = 0` on every row touched,
6. resolving the superseded rejection, if any.

A partial apply would leave a character whose stats disagree with their own food
log — unrecoverable without a manual audit — so no path through that function
commits only some of it.

### Concurrency

SQLite permits exactly one writer. Rather than let goroutines collide and retry
on `SQLITE_BUSY`, `store.DB` holds **two pools over one file**:

- **writer** — `MaxOpenConns(1)`, DSN `_txlock=immediate`. The pool is the queue:
  contention becomes a bounded wait for a connection instead of a storm of failed
  transactions. `BEGIN IMMEDIATE` takes the write lock up front, avoiding the
  read-then-upgrade deadlock that SQLite can only answer with `SQLITE_BUSY` —
  the classic bug that appears under load and never in testing.
- **reader** — `MaxOpenConns(N)`. Under WAL, readers never block the writer and
  the writer never blocks readers.

`store.WithTx` defers its rollback rather than writing one per error path, because
the failure that matters is the easy-to-miss one: an early return or a panic in
game-rule code leaving the connection inside a transaction. With a single writer
connection, one leaked transaction deadlocks every subsequent write for the life
of the process.

Base stats are written as **relative deltas** (`con_micro = MAX(?, con_micro + ?)`),
so the statement stays correct regardless of what committed between the read and
the write.

### Fixed-point stats

Stats accrue in thousandths (`MicroPerPoint = 1000`). A game economy that
accumulates and compares across millions of cycles cannot afford float drift
between client and server.

CON tracks protein counted toward the daily goal; VIT tracks calories, with a
penalty past `OvershootTolerance` (115% of goal). Both use `clampCounted`, which
only credits movement *toward* the goal — so logging 10,000 kcal of junk cannot
out-earn logging exactly the target. `domain.ComputeProgression` is pure, which is
what lets a PvP state-check server replay an entry log and verify a stat line
independently.

### Streaks are local-calendar

Streaks use the user's IANA zone, never UTC. A 9pm dinner in UTC-8 belongs to that
user's today. `cmd/api` imports `time/tzdata` to embed the zone database, because
Windows ships no zone files and `LoadLocation` would otherwise silently fall back
to UTC and break streaks for every user west of Greenwich.

### Firebase sync staging

Every mutable table carries `updated_at` + `is_synced`. `store.PendingSync` reads
dirty rows from the **reader** pool so a long sweep never blocks intake.
`MarkSynced` guards on the `updated_at` it saw: if the row changed again while the
sweep was in flight, the UPDATE matches nothing and the row correctly stays dirty
for the next pass. Clearing the flag unconditionally would drop that edit.

`SweepOnce` is wired and tested except for the `push` callback — **the Firebase
client itself is not implemented.**

---

## 2. The layered pixel-art avatar

### Composite model

The avatar is a stack of independent transparent sprite sheets, drawn back to
front. Each layer is a PNG with alpha everywhere it does not draw, so compositing
needs no masking and no per-layer geometry — just N draws of the same destination
rect in slot order.

Draw order is the declaration order of `EquipmentSlot`. Encoding z-order in the
type rather than a separate index field means a new slot cannot be added without
deciding where it sits, and no two layers can carry contradictory z-indices.

All sheets share one frame grid and one registration point, so frame N of the
torso lines up with frame N of the legs by construction.

### Animation

`kStanceClips` maps each `Stance` to a row of every sheet:

| Stance    | Row | Frames | FPS |
|-----------|-----|--------|-----|
| `idle`    | 0   | 4      | 6   |
| `jogging` | 1   | 8      | 12  |
| `lifting` | 2   | 6      | 9   |
| `combat`  | 3   | 6      | 14  |

Frame selection floors elapsed time — pixel-art animation is stepped, not
interpolated. Easing it would produce sub-frame blending that reads as mush at
this resolution.

`AnimationClock` is a `ValueNotifier<Duration>` passed to
`CustomPainter(repaint:)`, so the compositor re-invokes the painter directly
without rebuilding the widget tree. Calling `setState` per tick would rebuild and
re-lay-out the subtree at display rate to change nothing but a source rect.

### Pixel-art correctness

- `FilterQuality.none`, `isAntiAlias = false` — nearest-neighbour, one shared
  `Paint` reused across every layer and frame.
- **Integer scale only.** At 2.37x the remainder lands unevenly — some source
  pixels cover 2 device pixels, others 3 — and the sprite visibly wobbles.
- Destination origin floored to whole pixels. A half-pixel offset is invisible on
  a photo and ruinous here: every edge shimmers as the sprite animates.
- Source rects are exact integers, so the sampler never reads a neighbouring
  frame's edge pixel.
- Horizontal flip mirrors about the sprite's centre, not the canvas origin.

### The visibility toggle

The invariant: **hiding a layer changes only what is painted.**

Two different predicates, deliberately never merged:

```dart
bool get countsTowardPower => isEquipped;              // stat panel, PvP
bool get shouldDraw        => isEquipped && isVisible; // draw loop
```

`CharacterState.totalPowerLevel` filters on the first; `drawableLayers` filters on
the second. A hidden layer never reaches the draw loop at all — no atlas lookup,
no source rect, no draw call — while its `powerLevel` stays in the model and is
still summed. Filtering both on the same flag is the exact bug this design exists
to prevent: players losing power by transmogging.

Unequipping is the separate operation that *does* remove power.

Covered by `app/test/character_state_test.dart`.

---

## Running it

Verified on Go 1.27.0 and Flutter 3.47.2 / Dart 3.13.2.

```bash
cd server && go build ./... && go test ./...
```

```bash
cd app && flutter pub get && flutter test && flutter analyze
```

### Toolchain setup

Go is in winget:

```bash
winget install --id GoLang.Go --source winget
```

Flutter is **not** in winget — `Google.Flutter`, `Flutter.Flutter` and
`Google.FlutterSDK` do not exist, and `Google.DartSDK` is Dart alone. Clone it:

```bash
git clone https://github.com/flutter/flutter.git -b stable C:/flutter
```

`go build`/`go test` need nothing but Go — the SQLite driver is `modernc.org/sqlite`,
pure Go, chosen so no cgo/gcc is required. `flutter test`/`flutter analyze` need
nothing but the Flutter SDK. Only `flutter run` needs a platform toolchain
(Visual Studio "Desktop development with C++" for Windows; Android SDK for a phone).

### Test coverage

| Suite | Tests | What it covers |
|---|---|---|
| `internal/domain` | 9 | Stat economy: goal clamping, non-farmability, manual XP penalty, streak transitions, level-up carry |
| `internal/service` | 11 | Path A atomicity, Path B reasoning + no side effects, manual override pedigree, reattempt linking, double-resolve refusal, idempotent replay, concurrent writes, sync sweep + failed-push safety |
| `app/test` | 16 | Draw-call counting via a recording canvas, hidden-layer skip, slot draw order, stance row selection, frame advance and loop wrap, composite frame alignment, missing-sheet tolerance, widget lifecycle |

`TestConcurrentSubmitsSerialiseCleanly` exercises the two-pool design — 20
goroutines against one SQLite file, asserting no lost updates. Run 50x clean, and
25x clean under `-race` (500 concurrent submits).

`a hidden layer issues NO draw call` counts real `drawImageRect` calls through a
`_RecordingCanvas` rather than trusting the model. It has been mutation-tested:
changing `shouldDraw` to ignore `isVisible` makes it fail with `Expected: <2>
Actual: <3>`, so it genuinely catches the regression it exists to prevent.

### Running the race detector

Needs cgo, so a C compiler must be on PATH:

```bash
winget install --id BrechtSanders.WinLibs.POSIX.UCRT --source winget
```

```bash
CGO_ENABLED=1 go test -race ./...
```

### API

```
GET  /healthz
POST /v1/intake/photo    { client_entry_id, photo_uri, vision: {...},
                           supersedes_rejection_id? }
POST /v1/intake/manual   { client_entry_id, kcal, protein_g, carbs_g, fat_g,
                           supersedes_rejection_id? }
```

Both return `201` with the entry, day totals, remaining macros, character stat
line, and streak. Path B returns `422`:

```json
{
  "code": "photo_rejected",
  "validation_reasoning": "This appears to be a photo of a desk, not a meal.",
  "rejection_id": "rej_...",
  "can_retry_photo": true,
  "can_enter_manual": true
}
```

`client_entry_id` is an idempotency key — a retry over a flaky link returns the
original result (`"replayed": true`) rather than double-logging.

---

## Not done yet

- **Auth is a placeholder.** `bearerUserID` in `cmd/api/main.go` trusts the bearer
  token as a user ID. Any client can act as any user. Replace with Firebase Auth
  ID token verification before this is reachable off localhost.
- **No Firebase client.** `SweepOnce` needs its `push` callback implemented and a
  ticker to drive it.
- **No user/character provisioning.** The schema and intake path assume rows in
  `users` and `characters`; there is no signup endpoint yet.
- **No real sprite sheets.** `app/lib/main.dart` synthesises placeholder sheets so
  the renderer runs before the art exists. `art/aseprite/Sprite-0001.aseprite` is
  the only source art carried over.
- **No HTTP-layer tests.** `internal/httpapi` is exercised only indirectly; the
  422 body shape and the auth gate are unverified.
- **No golden-image test.** Draw calls are asserted, but nothing checks the
  rasterised output, so a scaling or registration regression would pass.
