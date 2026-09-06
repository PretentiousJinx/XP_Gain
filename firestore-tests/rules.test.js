// Security-rules tests, run against the Firestore emulator.
//
//   cd firestore-tests && npm install && npm test
//
// npm test wraps these in `firebase emulators:exec`, which needs a JDK.

import { readFileSync } from 'node:fs';
import { after, before, describe, it } from 'node:test';
import {
  assertFails,
  assertSucceeds,
  initializeTestEnvironment,
} from '@firebase/rules-unit-testing';
import {
  collection,
  doc,
  deleteDoc,
  getDoc,
  getDocs,
  setDoc,
  updateDoc,
} from 'firebase/firestore';

const ALICE = 'alice-uid';
const BOB = 'bob-uid';

let testEnv;

before(async () => {
  testEnv = await initializeTestEnvironment({
    projectId: 'demo-xpgain',
    firestore: {
      rules: readFileSync('../firestore.rules', 'utf8'),
      host: '127.0.0.1',
      port: 8081,
    },
  });

  // Seed as the server would, bypassing rules the same way a service account
  // does in production.
  await testEnv.withSecurityRulesDisabled(async (ctx) => {
    const db = ctx.firestore();
    for (const uid of [ALICE, BOB]) {
      await setDoc(doc(db, `users/${uid}`), { id: uid, goal_kcal: 2000 });
      await setDoc(doc(db, `users/${uid}/character/state`), { level: 3, con_micro: 5400 });
      await setDoc(doc(db, `users/${uid}/streak/state`), { current_streak: 4 });
      await setDoc(doc(db, `users/${uid}/entries/e1`), { kcal: 650, is_manual: 1 });
      await setDoc(doc(db, `users/${uid}/rejections/r1`), { validation_reasoning: 'not food' });
      await setDoc(doc(db, `users/${uid}/dailyTotals/2026-09-05`), { kcal: 650 });
    }
  });
});

after(async () => {
  await testEnv?.cleanup();
});

const asAlice = () => testEnv.authenticatedContext(ALICE).firestore();
const asBob = () => testEnv.authenticatedContext(BOB).firestore();
const asAnon = () => testEnv.unauthenticatedContext().firestore();

describe('reads', () => {
  it('a user can read their own profile', async () => {
    await assertSucceeds(getDoc(doc(asAlice(), `users/${ALICE}`)));
  });

  it('a user can read their own character, streak and entries', async () => {
    const db = asAlice();
    await assertSucceeds(getDoc(doc(db, `users/${ALICE}/character/state`)));
    await assertSucceeds(getDoc(doc(db, `users/${ALICE}/streak/state`)));
    await assertSucceeds(getDoc(doc(db, `users/${ALICE}/entries/e1`)));
    await assertSucceeds(getDoc(doc(db, `users/${ALICE}/dailyTotals/2026-09-05`)));
  });

  it('a user can list their own entries', async () => {
    // Safe because the uid is pinned by the path: the query cannot range
    // outside the caller's own subtree.
    await assertSucceeds(getDocs(collection(asAlice(), `users/${ALICE}/entries`)));
  });

  it('a user cannot read another user profile', async () => {
    await assertFails(getDoc(doc(asBob(), `users/${ALICE}`)));
  });

  it('a user cannot read another user character or entries', async () => {
    const db = asBob();
    await assertFails(getDoc(doc(db, `users/${ALICE}/character/state`)));
    await assertFails(getDoc(doc(db, `users/${ALICE}/entries/e1`)));
    await assertFails(getDocs(collection(db, `users/${ALICE}/entries`)));
  });

  it('an unauthenticated caller can read nothing', async () => {
    const db = asAnon();
    await assertFails(getDoc(doc(db, `users/${ALICE}`)));
    await assertFails(getDoc(doc(db, `users/${ALICE}/character/state`)));
    await assertFails(getDocs(collection(db, `users/${ALICE}/entries`)));
  });

  it('nobody can enumerate the users collection', async () => {
    // A `list` over /users would return every account in the project.
    await assertFails(getDocs(collection(asAlice(), 'users')));
    await assertFails(getDocs(collection(asAnon(), 'users')));
  });
});

describe('writes are server-only', () => {
  // The rule that matters most. Every stat in this database is derived by the
  // Go service from a food log it validated. A client that can write its own
  // character document can award itself any power level it likes, and the
  // pedigree the PvP state-check servers depend on becomes meaningless.

  it('a user cannot inflate their own character stats', async () => {
    await assertFails(
      updateDoc(doc(asAlice(), `users/${ALICE}/character/state`), { con_micro: 99999999 }),
    );
    await assertFails(
      setDoc(doc(asAlice(), `users/${ALICE}/character/state`), { level: 99, con_micro: 99999999 }),
    );
  });

  it('a user cannot fabricate a food entry', async () => {
    await assertFails(
      setDoc(doc(asAlice(), `users/${ALICE}/entries/forged`), { kcal: 1, protein_g: 500 }),
    );
  });

  it('a user cannot rewrite pedigree on an existing entry', async () => {
    // Flipping is_manual to 0 would launder a typed entry as AI-verified.
    await assertFails(
      updateDoc(doc(asAlice(), `users/${ALICE}/entries/e1`), { is_manual: 0 }),
    );
  });

  it('a user cannot extend their own streak', async () => {
    await assertFails(
      updateDoc(doc(asAlice(), `users/${ALICE}/streak/state`), { current_streak: 365 }),
    );
  });

  it('a user cannot raise their own macro goals to make adherence trivial', async () => {
    await assertFails(updateDoc(doc(asAlice(), `users/${ALICE}`), { goal_kcal: 1 }));
  });

  it('a user cannot delete their own records', async () => {
    await assertFails(deleteDoc(doc(asAlice(), `users/${ALICE}/entries/e1`)));
    await assertFails(deleteDoc(doc(asAlice(), `users/${ALICE}`)));
  });

  it('a user cannot resolve a rejection to launder its pedigree', async () => {
    await assertFails(
      updateDoc(doc(asAlice(), `users/${ALICE}/rejections/r1`), {
        resolved_by_entry_id: 'anything',
      }),
    );
  });

  it('a user cannot write into another user subtree', async () => {
    await assertFails(setDoc(doc(asBob(), `users/${ALICE}/entries/x`), { kcal: 0 }));
  });

  it('an unauthenticated caller cannot write', async () => {
    await assertFails(setDoc(doc(asAnon(), `users/${ALICE}/entries/x`), { kcal: 0 }));
  });
});

describe('collections outside the user tree', () => {
  it('are denied entirely', async () => {
    const db = asAlice();
    await assertFails(getDoc(doc(db, 'admin/config')));
    await assertFails(setDoc(doc(db, 'admin/config'), { x: 1 }));
    await assertFails(getDocs(collection(db, 'leaderboard')));
    await assertFails(setDoc(doc(db, 'leaderboard/top'), { x: 1 }));
  });
});
