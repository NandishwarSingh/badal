// Sync: drain the outbox, then pull the change feed.
//
// Order matters. Pushing first means the server's feed already reflects our
// local edits by the time we pull, so we never briefly overwrite our own work
// with a stale server copy.

import { Notes, Tasks, Timetable, Sync, ApiError } from "./api.js";
import { cache, taskCache, timetableCache, outbox, meta } from "./store.js";

export const state = $state({
  online: typeof navigator !== "undefined" ? navigator.onLine : true,
  syncing: false,
  pending: 0,
  lastError: "",
  lastSync: 0,
  conflicts: 0,
});

export async function refreshPending() {
  state.pending = await outbox.count();
}

// Queue a note write. Applied to the local cache immediately so the UI never
// waits on the network -- the outbox is what makes that safe.
export async function queuePut(note) {
  const local = { ...note, _dirty: true, updated_at: new Date().toISOString() };
  await cache.put(local);
  await outbox.add({ kind: "put", slug: note.slug, payload: note });
  await refreshPending();
  if (state.online) void run();
  return local;
}

export async function queueDelete(slug) {
  await cache.del(slug);
  await outbox.add({ kind: "del", slug });
  await refreshPending();
  if (state.online) void run();
}

// Task writes queue the same way note writes do. The id is generated on the
// client so a task captured in aeroplane mode already carries the identity the
// server will store it under, and the outbox entry stays valid across the flight.
export async function queueTask(task) {
  const local = { ...task, _dirty: true, updated_at: new Date().toISOString() };
  await taskCache.put(local);
  // Not `id`: the outbox store's keyPath is `id` with autoIncrement, so
  // putting a uuid there would hijack the primary key and break the
  // numeric ordering that keeps writes in sequence.
  await outbox.add({
    entity: "task",
    kind: "put",
    taskId: task.id,
    payload: task,
  });
  await refreshPending();
  if (state.online) void run();
  return local;
}

export async function queueTaskDelete(id) {
  await taskCache.del(id);
  await outbox.add({ entity: "task", kind: "del", taskId: id });
  await refreshPending();
  if (state.online) void run();
}

// Timetable slots queue exactly like tasks: cache first so the grid updates on
// tap, and a client-generated id keeps the outbox entry valid offline.
export async function queueSlot(slot) {
  const local = { ...slot, _dirty: true, updated_at: new Date().toISOString() };
  await timetableCache.put(local);
  await outbox.add({ entity: "slot", kind: "put", slotId: slot.id, payload: slot });
  await refreshPending();
  if (state.online) void run();
  return local;
}

export async function queueSlotDelete(id) {
  await timetableCache.del(id);
  await outbox.add({ entity: "slot", kind: "del", slotId: id });
  await refreshPending();
  if (state.online) void run();
}

let running = false;

export async function run() {
  if (running || !state.online) return;
  running = true;
  state.syncing = true;
  state.lastError = "";
  try {
    await push();
    await pull();
    state.lastSync = Date.now();
  } catch (e) {
    state.lastError = e?.message || String(e);
  } finally {
    state.syncing = false;
    running = false;
    await refreshPending();
  }
}

async function push() {
  const items = await outbox.all();
  items.sort((a, b) => a.id - b.id);
  for (const item of items) {
    try {
      // Entity is absent on rows queued before tasks existed; those are
      // all notes, so the default keeps an in-flight queue working across
      // the upgrade rather than wedging on unknown items.
      if (item.entity === "task") {
        if (item.kind === "put") {
          const saved = await Tasks.put(item.payload);
          await taskCache.put({ ...saved.task, _dirty: false });
        } else {
          await Tasks.del(item.taskId).catch((e) => {
            if (!(e instanceof ApiError && e.status === 404)) throw e;
          });
        }
        await outbox.remove(item.id);
        continue;
      }
      if (item.entity === "slot") {
        if (item.kind === "put") {
          const saved = await Timetable.putSlot(item.payload);
          await timetableCache.put({ ...saved.slot, _dirty: false });
        } else {
          await Timetable.delSlot(item.slotId).catch((e) => {
            if (!(e instanceof ApiError && e.status === 404)) throw e;
          });
        }
        await outbox.remove(item.id);
        continue;
      }
      if (item.kind === "put") {
        const saved = await Notes.put(item.slug, item.payload);
        await cache.put({ ...saved, _dirty: false });
      } else if (item.kind === "del") {
        await Notes.del(item.slug).catch((e) => {
          // Already gone server-side is success, not failure.
          if (!(e instanceof ApiError && e.status === 404)) throw e;
        });
      }
      await outbox.remove(item.id);
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        // The edit is now parked on the note as a conflict. Drop the
        // queued write: retrying would record the same conflict again
        // on every attempt.
        await outbox.remove(item.id);
        const fresh = await Notes.get(item.slug).catch(() => null);
        if (fresh?.note) {
          await cache.put({
            ...fresh.note,
            _dirty: false,
            _conflicts: (fresh.conflicts || []).length,
          });
        }
        continue;
      }
      if (
        e instanceof ApiError &&
        e.status >= 400 &&
        e.status < 500 &&
        e.status !== 429
      ) {
        // Permanently bad request: keeping it would wedge the queue.
        await outbox.remove(item.id);
        state.lastError = `dropped bad write for ${item.slug ?? item.taskId}: ${e.message}`;
        continue;
      }
      throw e; // network or 5xx -- keep it queued and retry later
    }
  }
}

async function pull() {
  let cursor = await meta.get("cursor", 0);
  for (let guard = 0; guard < 50; guard++) {
    const res = await Sync.since(cursor);
    const changes = res.changes || [];
    if (changes.some((c) => c.entity_type === "note")) {
      // The feed carries ids, not content -- it is a doorbell. Fetch the
      // full list once rather than N times for N changes.
      const list = await Notes.list();
      await cache.clear();
      await cache.putMany(
        (list.notes || []).map((n) => ({ ...n, _dirty: false })),
      );
    }
    if (changes.some((c) => c.entity_type === "timetable_slot")) {
      // Same doorbell: one slot changed on another device, so refetch the
      // whole (small) timetable rather than reconciling per slot.
      const tt = await Timetable.get().catch(() => null);
      if (tt) {
        await timetableCache.replaceAll(
          (tt.slots || []).map((s) => ({ ...s, _dirty: false })),
        );
      }
    }
    cursor = res.cursor ?? cursor;
    await meta.set("cursor", cursor);
    if (!res.more) break;
  }
  const c = await Notes.conflicts().catch(() => null);
  if (c) state.conflicts = c.count || 0;
}

let autoSyncStarted = false;

export function startAutoSync() {
  if (typeof window === "undefined") return;
  // Idempotent: the layout starts sync from the remembered session and again
  // once the server confirms it, and a second set of listeners plus a second
  // 60-second timer would just double every poll.
  if (autoSyncStarted) return;
  autoSyncStarted = true;
  const on = () => {
    state.online = true;
    void run();
  };
  const off = () => {
    state.online = false;
  };
  window.addEventListener("online", on);
  window.addEventListener("offline", off);
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible") void run();
  });
  void run();
  setInterval(() => void run(), 60_000);
}
