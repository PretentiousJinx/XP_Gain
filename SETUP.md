# Setup

Everything in this repo is written and tested, but the identity paths have never
run against a real Firebase project. This is the list of what only you can do,
in the order that unblocks the most.

**Never paste a service-account key, an API key, or a signing keystore into a
chat.** Where a secret is involved below, put the file on disk and point an
environment variable at it. The only thing worth telling anyone is the project
ID, which is not secret.

---

## 1. Create the Firebase project

Console → **Add project**. Note the project ID (e.g. `xpgain-prod`).

```bash
FIREBASE_PROJECT_ID=xpgain-prod
```

This alone unblocks the Go server: it is the value checked against every ID
token's `aud` and `iss`, and the server refuses to start without it.

## 2. Generate the client config

```bash
dart pub global activate flutterfire_cli
cd app && flutterfire configure --project=xpgain-prod
```

Writes three files, all gitignored because they are yours, not the repo's:

- `app/lib/firebase_options.dart`
- `app/android/app/google-services.json`
- `app/ios/Runner/GoogleService-Info.plist`

Until these exist, `main.dart` logs a warning and falls back to the
`DEV_ID_TOKEN` path rather than crashing — so the app runs, it just cannot sign
anyone in.

**One code change is needed after this**, because `firebase_options.dart` does
not exist yet and cannot be imported:

```dart
// app/lib/main.dart
import 'firebase_options.dart';
...
await Firebase.initializeApp(options: DefaultFirebaseOptions.currentPlatform);
```

## 3. Turn on Email/Password

Console → **Authentication → Sign-in method → Email/Password → Enable**.

At this point sign-in, registration and password reset work. MFA does not.

## 4. Upgrade to Identity Platform, for SMS

**SMS multi-factor is not part of the free Firebase Auth tier.** It requires
Google Cloud Identity Platform, which needs the **Blaze (pay-as-you-go) plan**.

Console → **Authentication → Settings → Multi-factor authentication →
Upgrade**, then enable **SMS** as a second factor.

Costs money per SMS. Add test numbers under
**Authentication → Sign-in method → Phone → Phone numbers for testing** so
development does not send real messages.

Until this is done, leave the server's `-require-mfa` flag **off**. With it on
and SMS unavailable, every account is locked out of the API: enrolment is the
only way past the 403, and enrolment is what is unavailable.

## 5. Phone auth needs platform proof-of-origin

This is the step most likely to bite, because it fails only on a real device.

**Android** — Firebase verifies the app with Play Integrity, which needs your
signing certificate fingerprints registered:

```bash
cd app/android && ./gradlew signingReport
```

Copy the SHA-1 **and** SHA-256 into Console → Project settings → your Android
app → **Add fingerprint**. Do this for the debug key *and* whatever key you
ship with, or phone auth works in debug and fails in release.

**iOS** — phone auth uses a silent APNs push to verify the device:

- Upload an **APNs authentication key** (.p8, from the Apple Developer portal)
  under Console → Project settings → Cloud Messaging.
- Enable **Push Notifications** and **Background Modes → Remote notifications**
  in Xcode.
- `flutterfire configure` normally adds the reversed-client-ID URL scheme to
  `Info.plist`; confirm it is there.

## 6. Service-account key, for sync and revocation

Console → **Project settings → Service accounts → Generate new private key**.

**This file is a credential.** Save it outside the repo, or inside it only at a
gitignored path.

```bash
GOOGLE_APPLICATION_CREDENTIALS=/secure/path/xpgain-sa.json
```

The server checks the key's `project_id` matches `FIREBASE_PROJECT_ID` at boot,
so a key from the wrong project fails loudly instead of writing to a stranger's
database. Without this key, Firestore sync is off and revoked sessions stay
valid until their tokens expire — the server warns about both at startup.

## 7. Firestore

Console → **Firestore Database → Create database** (production mode).

```bash
firebase login
firebase deploy --only firestore:rules --project xpgain-prod
```

The rules are written and tested against the emulator; deploying is the only
part left. They deny **all** client writes, which is correct: the Go service is
the sole writer, and its service account bypasses rules entirely.

## 8. A device with a biometric enrolled

Face ID / fingerprint cannot be exercised in an emulator without one enrolled
(Android emulator: Settings → Security → Fingerprint, then
`adb -e emu finger touch 1`). The gate is written to open normally on hardware
that has none, so this only blocks testing the lock itself.

---

## Running it

```bash
cd server && FIREBASE_PROJECT_ID=xpgain-prod GOOGLE_APPLICATION_CREDENTIALS=/secure/path/sa.json go run ./cmd/api
```

Add `-require-mfa` once step 4 is done and you have enrolled a factor yourself —
otherwise you will lock your own account out.

```bash
cd app && flutter run --dart-define=API_BASE_URL=http://10.0.2.2:8080
```

`10.0.2.2` is the host loopback as seen from the Android emulator; `localhost`
there means the emulator itself. On a physical device use your machine's LAN IP,
and note that Android blocks cleartext HTTP by default — for local testing add a
network security config, or terminate TLS in front of the server.

---

## What I would want back

To take this further I only need non-secret facts:

- the project ID;
- whether step 4 (Identity Platform) is done, since `-require-mfa` is unsafe to
  enable before it;
- the output of a failing run — logs, or the `code` from an error response.

If something fails on a device, the server logs the rejection reason for every
refused token (it is deliberately never sent to the client), so that log usually
names the problem outright.
