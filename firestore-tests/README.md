# Firestore rules tests

Verifies `../firestore.rules` against the Firestore emulator.

```bash
npm install
npm test
```

`npm test` runs `firebase emulators:exec`, which starts a local Firestore and
needs a **JDK on PATH**. Nothing touches a real Firebase project: the emulator
runs against the fake project id `demo-xpgain`.

Seeding happens through `withSecurityRulesDisabled`, which is how the Go service
behaves in production -- a service account bypasses rules entirely, so the tests
exercise the same asymmetry the app relies on: the server writes, the client only
reads.
